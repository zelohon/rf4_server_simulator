package main

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

const defaultGameRegion = "CN"

type gameConfigRequest struct {
	Path string `json:"path"`
}

func (a *app) gameState() (string, string) {
	a.gameMu.RLock()
	defer a.gameMu.RUnlock()
	region := strings.TrimSpace(a.cfg.Launcher.GameRegion)
	if region == "" {
		region = defaultGameRegion
	}
	return a.cfg.Launcher.GamePath, region
}

func (a *app) handleGameConfig(w http.ResponseWriter, r *http.Request) {
	if !requireLocalJSONPost(w, r) {
		return
	}
	defer r.Body.Close()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req gameConfigRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": "参数错误: " + err.Error()})
		return
	}
	gamePath, region, err := a.saveGamePath(req.Path)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"path": gamePath, "region": region})
}

func (a *app) handleGameLaunch(w http.ResponseWriter, r *http.Request) {
	if !requireLocalJSONPost(w, r) {
		return
	}
	gamePath, region := a.gameState()
	cmd, err := buildGameCommand(gamePath, region)
	if err != nil {
		writeJSONStatus(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := a.startGameProcess(cmd); err != nil {
		writeJSONStatus(w, http.StatusInternalServerError, map[string]string{"error": "启动游戏失败: " + err.Error()})
		return
	}
	a.log.Info("已启动游戏: " + gamePath + " -region " + region)
	writeJSON(w, map[string]any{"ok": true, "path": gamePath, "region": region})
}

func (a *app) saveGamePath(rawPath string) (string, string, error) {
	gamePath, err := normalizeGameExecutable(rawPath)
	if err != nil {
		return "", "", err
	}
	_, region := a.gameState()
	region, err = normalizeGameRegion(region)
	if err != nil {
		return "", "", err
	}
	if a.cfgPath == "" {
		return "", "", fmt.Errorf("未指定配置文件路径")
	}
	if err := persistLauncherConfig(a.cfgPath, gamePath, region); err != nil {
		return "", "", fmt.Errorf("写入配置文件失败: %w", err)
	}
	a.gameMu.Lock()
	a.cfg.Launcher.GamePath = gamePath
	a.cfg.Launcher.GameRegion = region
	a.gameMu.Unlock()
	return gamePath, region, nil
}

func (a *app) startGameProcess(cmd *exec.Cmd) error {
	if a.startGame != nil {
		return a.startGame(cmd)
	}
	return cmd.Start()
}

func buildGameCommand(rawPath, rawRegion string) (*exec.Cmd, error) {
	gamePath, err := normalizeGameExecutable(rawPath)
	if err != nil {
		return nil, err
	}
	region, err := normalizeGameRegion(rawRegion)
	if err != nil {
		return nil, err
	}
	cmd := exec.Command(gamePath, "-region", region)
	cmd.Dir = filepath.Dir(gamePath)
	return cmd, nil
}

func normalizeGameExecutable(rawPath string) (string, error) {
	gamePath := strings.TrimSpace(rawPath)
	if len(gamePath) >= 2 {
		first, last := gamePath[0], gamePath[len(gamePath)-1]
		if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
			gamePath = strings.TrimSpace(gamePath[1 : len(gamePath)-1])
		}
	}
	if gamePath == "" {
		return "", fmt.Errorf("请填写 rf4_x64.exe 的完整路径")
	}
	if !filepath.IsAbs(gamePath) {
		return "", fmt.Errorf("必须填写绝对路径")
	}
	gamePath = filepath.Clean(gamePath)
	if !strings.EqualFold(filepath.Base(gamePath), "rf4_x64.exe") {
		return "", fmt.Errorf("文件名必须是 rf4_x64.exe")
	}
	info, err := os.Stat(gamePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("文件不存在: %s", gamePath)
		}
		return "", fmt.Errorf("无法检查文件: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("路径不是普通文件: %s", gamePath)
	}
	return gamePath, nil
}

func normalizeGameRegion(rawRegion string) (string, error) {
	region := strings.TrimSpace(rawRegion)
	if region == "" {
		return defaultGameRegion, nil
	}
	for _, ch := range region {
		if (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') || (ch >= '0' && ch <= '9') || ch == '_' || ch == '-' {
			continue
		}
		return "", fmt.Errorf("game_region 只能包含字母、数字、下划线或连字符")
	}
	return region, nil
}

func persistLauncherConfig(configPath, gamePath, region string) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return err
	}
	info, err := os.Stat(configPath)
	if err != nil {
		return err
	}
	var doc yaml.Node
	if strings.TrimSpace(string(data)) != "" {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return err
		}
	}
	if doc.Kind == 0 {
		doc.Kind = yaml.DocumentNode
	}
	if len(doc.Content) == 0 {
		doc.Content = append(doc.Content, &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"})
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("配置根节点不是 YAML 映射")
	}
	launcher := mappingValue(root, "launcher")
	if launcher == nil {
		root.Content = append(root.Content,
			&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "launcher"},
			&yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"},
		)
		launcher = root.Content[len(root.Content)-1]
	}
	if launcher.Kind != yaml.MappingNode {
		return fmt.Errorf("配置项 launcher 不是 YAML 映射")
	}
	setMappingValue(launcher, "game_path", gamePath)
	setMappingValue(launcher, "game_region", region)
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(configPath, out, info.Mode().Perm())
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func setMappingValue(mapping *yaml.Node, key, value string) {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value != key {
			continue
		}
		node := mapping.Content[i+1]
		node.Kind = yaml.ScalarNode
		node.Tag = "!!str"
		node.Value = value
		return
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
		&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: value},
	)
}

func requireLocalJSONPost(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSONStatus(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 POST"})
		return false
	}
	if !strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
		writeJSONStatus(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type 必须是 application/json"})
		return false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() {
		writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "仅允许本机控制中心调用"})
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || !strings.EqualFold(u.Host, r.Host) {
			writeJSONStatus(w, http.StatusForbidden, map[string]string{"error": "拒绝跨站请求"})
			return false
		}
	}
	return true
}

func writeJSONStatus(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
