package main

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/mac"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"sshtunnel/internal/logger"
	"sshtunnel/internal/proxy"
	"sshtunnel/internal/updater"
	"sshtunnel/internal/web"
)

//go:embed all:frontend/dist
var assets embed.FS

// 版本信息，构建时通过 -ldflags 注入：
//
//	go build -ldflags "-X main.version=v1.1.0 -X main.commit=$(git rev-parse --short HEAD) -X main.buildTime=$(date -u +%Y-%m-%dT%H:%M:%SZ) -X main.buildVariant=desktop"
//
// buildVariant: "web"（默认，无 GUI）或 "desktop"（Wails 桌面版），决定版本检查时匹配的资产后缀。
// 未注入时为默认值 dev/none/unknown/web。
var (
	version      = "dev"
	commit       = "none"
	buildTime    = "unknown"
	buildVariant = "web" // "web" 或 "desktop"
)

// 运行模式
type RunMode string

const (
	ModeDesktop RunMode = "desktop"
	ModeWeb     RunMode = "web"
	ModeBoth    RunMode = "both"
)

// CLI 参数
var (
	flagMode        = flag.String("mode", "desktop", "运行模式: desktop | web | both")
	flagWebHost     = flag.String("web-host", "127.0.0.1", "WEB 监听地址")
	flagWebPort     = flag.Int("web-port", 8080, "WEB 监听端口")
	flagAuth        = flag.String("auth", "", "启用密码访问，格式 user:password，留空则无密码")
	flagTLSCert     = flag.String("tls-cert", "", "TLS 证书路径")
	flagTLSKey      = flag.String("tls-key", "", "TLS 私钥路径")
	flagVersion     = flag.Bool("version", false, "打印版本信息并退出")
	flagCheckUpdate = flag.Bool("check-update", false, "检查 GitHub Release 是否有新版本并退出")
	flagRepo        = flag.String("repo", updater.DefaultRepo, "GitHub 仓库（owner/repo），用于版本检查与安装脚本")
	flagGeoIPDB     = flag.String("geoip-db", "", "GeoIP .mmdb 路径（Country 或 City）；留空按系统设置加载，传 none 禁用")
)

func main() {
	flag.Parse()

	// --version：仅打印版本信息后退出
	if *flagVersion {
		printVersion()
		return
	}

	// --check-update：查询最新发布并比较版本后退出
	if *flagCheckUpdate {
		os.Exit(runCheckUpdate(*flagRepo))
		return
	}

	mode := RunMode(*flagMode)
	switch mode {
	case ModeDesktop, ModeWeb, ModeBoth:
		// 合法模式
	default:
		fmt.Printf("无效的运行模式: %s（可选 desktop | web | both）\n", *flagMode)
		os.Exit(1)
	}

	// 解析鉴权配置（--auth 为启动参数，作为运行时设置的初始值/覆盖）
	_, authUser, authPass := parseAuth(*flagAuth)

	// 加载 GeoIP 数据库（用于代理防火墙的国家/城市规则）
	// 优先级：--geoip-db 外部文件 > 系统设置中启用的已下载 City 库 > 内置 Country 库。
	switch strings.ToLower(strings.TrimSpace(*flagGeoIPDB)) {
	case "none", "off", "disabled":
		log.Printf("已按 --geoip-db=%s 禁用 GeoIP（国家/城市规则不生效）", *flagGeoIPDB)
	case "":
		loadGeoIPBySettings()
	default:
		if err := proxy.LoadGeoIPDatabaseFile(*flagGeoIPDB); err != nil {
			log.Printf("⚠️  GeoIP 数据库加载失败（国家/城市规则将不生效）: %v", err)
		} else {
			log.Printf("GeoIP 数据库已加载: %s (%s)", *flagGeoIPDB, proxy.CurrentGeoIPStatus().Type)
		}
	}

	// 初始化共享业务层
	app := NewApp()
	app.SetVersion(version)

	// WEB 模式或混合模式：启动 WEB 服务
	if mode == ModeWeb || mode == ModeBoth {
		startWebServer(app, authUser, authPass)
	}

	// 桌面模式或混合模式：启动 Wails
	if mode == ModeDesktop || mode == ModeBoth {
		startDesktop(app)
		return
	}

	// 仅 WEB 模式：阻塞等待信号
	log.Println("WEB 模式运行中，按 Ctrl+C 退出")
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("正在关闭...")
	if app.tunnelMgr != nil {
		app.tunnelMgr.StopAll()
	}
}

// parseAuth 解析 --auth=user:password
func parseAuth(s string) (enabled bool, user, pass string) {
	if s == "" {
		return false, "", ""
	}
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 || parts[0] == "" {
		log.Fatalf("无效的 --auth 参数，格式应为 user:password")
	}
	return true, parts[0], parts[1]
}

// generateJWTSecret 生成随机 JWT 密钥
func generateJWTSecret() []byte {
	b := make([]byte, 32)
	rand.Read(b)
	return []byte(hex.EncodeToString(b))
}

// startWebServer 启动 WEB 服务器
func startWebServer(app *App, authUser, authPass string) {
	// 初始化业务层
	if err := app.Init(); err != nil {
		log.Fatalf("业务层初始化失败: %v", err)
	}

	// 创建 WebSocket Hub
	hub := web.NewHub()

	// 注册日志 sink → 广播到所有 WS 客户端
	app.logger.AddSink(func(entry logger.Entry) {
		hub.Broadcast(web.WSMessage{Type: "log", Data: entry})
	})

	// 注册隧道状态变更 → 广播（同时保留桌面端 EventsEmit 在 startup 中处理）
	app.tunnelMgr.OnStatusChange(func(tunnelID, status string) {
		hub.Broadcast(web.WSMessage{
			Type: "status",
			Data: map[string]string{
				"tunnel_id": tunnelID,
				"status":    status,
			},
		})
	})

	// 创建 API Handler
	handler := web.NewHandler(app.cfgMgr, app.tunnelMgr, app.logger,
		generateJWTSecret(), "", "", hub).
		WithVersion(version, commit, buildVariant, *flagRepo)

	// 加载持久化的访问密码设置；--auth 启动参数覆盖（仅内存）
	handler.UsePersistentAuth(authUser, authPass)
	// --tls-cert/--tls-key 启动参数：启用并持久化 WEB 面板 HTTPS
	handler.UseCLITLS(*flagTLSCert, *flagTLSKey)

	// 创建并启动服务器
	srv := web.NewServer(web.Config{
		Host:      *flagWebHost,
		Port:      *flagWebPort,
		JWTSecret: handler.JWTSecret(),
	}, handler)

	go func() {
		if err := srv.Start(); err != nil {
			log.Printf("WEB 服务器错误: %v", err)
		}
	}()
}

// startDesktop 启动 Wails 桌面应用
func startDesktop(app *App) {
	err := wails.Run(&options.App{
		Title:     "SSHTunnel",
		Width:     1280,
		Height:    820,
		MinWidth:  960,
		MinHeight: 640,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 24, G: 27, B: 31, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
		Mac: &mac.Options{
			TitleBar: mac.TitleBarHiddenInset(),
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	if err := a.Init(); err != nil {
		a.logger.Error("初始化失败: " + err.Error())
		return
	}
	// 连接日志实时推送到桌面端
	a.logger.AddSink(func(entry logger.Entry) {
		runtime.EventsEmit(ctx, "log:new", entry)
	})
	// 隧道状态变更通知桌面端
	if a.tunnelMgr != nil {
		a.tunnelMgr.OnStatusChange(func(tunnelID string, status string) {
			runtime.EventsEmit(ctx, "tunnel:status", map[string]string{
				"tunnel_id": tunnelID,
				"status":    status,
			})
		})
	}
}

func (a *App) shutdown(ctx context.Context) {
	if a.tunnelMgr != nil {
		a.tunnelMgr.StopAll()
	}
}

// loadGeoIPBySettings 依据系统设置决定加载已下载的 City 库还是内置 Country 库。
// 城市库缺失或损坏时自动回退内置库，保证国家规则始终可用。
func loadGeoIPBySettings() {
	if s := proxy.LoadGeoIPSettings(); s.Enabled {
		path := proxy.CityDatabasePath()
		if err := proxy.LoadGeoIPDatabaseFile(path); err != nil {
			log.Printf("⚠️  已启用城市数据库但加载失败，回退内置 Country 库: %v", err)
			if err2 := proxy.LoadBuiltinGeoDatabase(); err2 != nil {
				log.Printf("⚠️  内置 GeoIP 数据库加载失败（国家规则将不生效）: %v", err2)
			}
			return
		}
		log.Printf("已加载城市级 GeoIP 数据库: %s", path)
		return
	}
	if err := proxy.LoadBuiltinGeoDatabase(); err != nil {
		log.Printf("⚠️  内置 GeoIP 数据库加载失败（国家规则将不生效）: %v", err)
		return
	}
	log.Printf("已加载内置 GeoIP 数据库（GeoLite2-Country）")
}

// printVersion 打印版本信息。
func printVersion() {
	fmt.Printf("SSHTunnel %s\n", version)
	fmt.Printf("  commit:     %s\n", commit)
	fmt.Printf("  build time: %s\n", buildTime)
	fmt.Printf("  variant:    %s\n", buildVariant)
	fmt.Printf("  repo:       %s\n", updater.DefaultRepo)
}

// runCheckUpdate 查询 GitHub 最新发布并与当前版本比较。
// 退出码：0 表示已是最新或无更新；1 表示有新版本；2 表示检查失败（网络/API 错误）。
func runCheckUpdate(repo string) int {
	fmt.Printf("当前版本: %s (%s)\n", version, buildVariant)
	fmt.Printf("正在检查 %s 的最新发布...\n", repo)
	latest, updateAvailable, rel, err := updater.CheckUpdate(version, repo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "检查更新失败: %v\n", err)
		return 2
	}
	fmt.Printf("最新版本: %s\n", latest)
	if rel != nil && rel.HTMLURL != "" {
		fmt.Printf("发布页面: %s\n", rel.HTMLURL)
	}
	if !updateAvailable {
		fmt.Println("已是最新版本。")
		return 0
	}
	fmt.Println("发现新版本！请前往发布页面下载，或运行安装脚本升级：")
	if rel != nil {
		// 提示当前平台与变体对应的二进制资产
		if u, err := rel.AssetForPlatform("", "", buildVariant); err == nil {
			fmt.Printf("  当前平台下载地址: %s\n", u)
		}
	}
	return 1
}
