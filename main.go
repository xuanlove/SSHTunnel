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

	"sshsuidao/internal/logger"
	"sshsuidao/internal/web"
)

//go:embed all:frontend/dist
var assets embed.FS

// 运行模式
type RunMode string

const (
	ModeDesktop RunMode = "desktop"
	ModeWeb     RunMode = "web"
	ModeBoth    RunMode = "both"
)

// CLI 参数
var (
	flagMode     = flag.String("mode", "desktop", "运行模式: desktop | web | both")
	flagWebHost  = flag.String("web-host", "127.0.0.1", "WEB 监听地址")
	flagWebPort  = flag.Int("web-port", 8080, "WEB 监听端口")
	flagAuth     = flag.String("auth", "", "启用密码访问，格式 user:password，留空则无密码")
	flagTLSCert  = flag.String("tls-cert", "", "TLS 证书路径")
	flagTLSKey   = flag.String("tls-key", "", "TLS 私钥路径")
)

func main() {
	flag.Parse()

	mode := RunMode(*flagMode)
	switch mode {
	case ModeDesktop, ModeWeb, ModeBoth:
		// 合法模式
	default:
		fmt.Printf("无效的运行模式: %s（可选 desktop | web | both）\n", *flagMode)
		os.Exit(1)
	}

	// 解析鉴权配置
	authEnabled, authUser, authPass := parseAuth(*flagAuth)

	// 初始化共享业务层
	app := NewApp()

	// WEB 模式或混合模式：启动 WEB 服务
	if mode == ModeWeb || mode == ModeBoth {
		startWebServer(app, authEnabled, authUser, authPass)
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
func startWebServer(app *App, authEnabled bool, authUser, authPass string) {
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
		generateJWTSecret(), authUser, authPass, hub)

	// 创建并启动服务器
	srv := web.NewServer(web.Config{
		Host:        *flagWebHost,
		Port:        *flagWebPort,
		AuthEnabled: authEnabled,
		Username:    authUser,
		Password:    authPass,
		JWTSecret:   handler.JWTSecret(),
		TLSCert:     *flagTLSCert,
		TLSKey:      *flagTLSKey,
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
		Title:     "SSH Tunnel Manager",
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
