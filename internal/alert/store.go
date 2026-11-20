package alert

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// Data 告警配置持久化结构
type Data struct {
	Rules   []Rule  `json:"rules"`
	Webhook Webhook `json:"webhook"`
}

// fileStore alerts.json 持久化（0600 原子写）
type fileStore struct {
	path string
}

func newFileStore(dir string) (*fileStore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	return &fileStore{path: filepath.Join(dir, "alerts.json")}, nil
}

// Load 读取配置；文件不存在时返回空配置
func (s *fileStore) Load() (Data, error) {
	var d Data
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return d, nil
		}
		return d, err
	}
	if len(b) == 0 {
		return d, nil
	}
	if err := json.Unmarshal(b, &d); err != nil {
		return d, err
	}
	return d, nil
}

// Save 原子写入配置
func (s *fileStore) Save(d Data) error {
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".alerts-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, s.path)
}
