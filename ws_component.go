// ws_component 是《万国觉醒·统帅远征》后端自定义的网络连接器组件。
//
// 为什么不用 cherry 内置的 pomelo 连接器？
//   - 前端 wgjx-net.js 采用「一行一条 JSON」的极简协议（与 ddz-net 同源），
//     并内置 localStorage 离线降级；为不改动已验证可用的前端，后端也用同协议。
//   - cherry 的 WS 连接器底层依赖 gorilla/websocket（已是 cherry 的间接依赖），
//     这里直接复用该库，按行-JSON 收发，保持与前端完全线兼容。
//
// 该组件实现 cherry 的 IComponent 生命周期，由 cherry 应用统一托管启停。
package main

import (
	"encoding/json"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	cfacade "github.com/cherry-game/cherry/facade"
	clog "github.com/cherry-game/cherry/logger"
	"github.com/gorilla/websocket"
)

// WsComponent 自定义原始 WebSocket 组件（行-JSON 协议）。
//
// 同源部署：同一端口既托管 H5 静态页（根路径 / 返回游戏页，满足健康检查），
// 又在 /wgjx-ws 提供 WebSocket 升级（匹配前端 wgjx.vue 的 resolveUrl），
// 避免跨域与网关透传问题，保证联网排行榜开箱可用。
type WsComponent struct {
	cfacade.Component
	store     *Store
	addr      string
	staticDir string
	upgrader  websocket.Upgrader
	server    *http.Server
	mu        sync.Mutex
}

// NewWsComponent 构造组件实例。
//   - addr：监听地址，如 :9105
//   - staticDir：H5 构建产物目录（为空则不托管静态文件，仅提供 WS）
func NewWsComponent(store *Store, addr string, staticDir string) *WsComponent {
	return &WsComponent{
		store:     store,
		addr:      addr,
		staticDir: staticDir,
		upgrader: websocket.Upgrader{
			ReadBufferSize:  1024,
			WriteBufferSize: 1024,
			CheckOrigin:     func(_ *http.Request) bool { return true },
		},
	}
}

// Name 组件唯一名。
func (c *WsComponent) Name() string { return "wgjx_ws" }

// OnAfterInit 在所有组件 Init 之后启动 HTTP/WS 监听（独立 goroutine，不阻塞主流程）。
func (c *WsComponent) OnAfterInit() {
	mux := http.NewServeMux()
	// WebSocket 升级端点：匹配前端 wgjx.vue resolveUrl 的 /wgjx-ws。
	mux.HandleFunc("/wgjx-ws", c.handleWs)
	// 同源托管 H5 静态页（根路径提供游戏页，满足发布平台 HTTP 探活）。
	if c.staticDir != "" {
		mux.HandleFunc("/", c.handleStatic)
	}
	c.server = &http.Server{Addr: c.addr, Handler: mux}
	go func() {
		clog.Infof("[wgjx] http+websocket listening at %s (staticDir=%q)", c.addr, c.staticDir)
		if err := c.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			clog.Errorf("[wgjx] http server error: %v", err)
		}
	}()
}

// handleStatic 提供 H5 静态资源，并对无扩展名的路由回退到 index.html（SPA 兼容）。
func (c *WsComponent) handleStatic(w http.ResponseWriter, r *http.Request) {
	upath := r.URL.Path
	if upath == "" || upath == "/" {
		upath = "/index.html"
	}
	// 防目录穿越：先把请求路径规整到以 "/" 开头的相对路径，再 Join 到 staticDir。
	clean := path.Clean("/" + strings.TrimPrefix(upath, "/"))
	full := filepath.Join(c.staticDir, clean)
	// 二次校验：确保最终路径仍处于 staticDir 之内。
	absRoot, _ := filepath.Abs(c.staticDir)
	absFull, _ := filepath.Abs(full)
	if !strings.HasPrefix(absFull, absRoot+string(os.PathSeparator)) && absFull != absRoot {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		// 文件不存在或为目录：SPA 回退（仅对无扩展名路径，避免误吞真实静态资源）。
		if filepath.Ext(upath) == "" {
			http.ServeFile(w, r, filepath.Join(c.staticDir, "index.html"))
			return
		}
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, full)
}

// OnStop 关闭监听并落盘。
func (c *WsComponent) OnStop() {
	if c.server != nil {
		_ = c.server.Close()
	}
	_ = c.store.Persist()
}

// handleWs 处理一次 WebSocket 升级后的连接：循环读取消息并按行解析 JSON。
func (c *WsComponent) handleWs(w http.ResponseWriter, r *http.Request) {
	conn, err := c.upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			var msg struct {
				Cmd  string          `json:"cmd"`
				Data json.RawMessage `json:"data"`
			}
			if err = json.Unmarshal([]byte(line), &msg); err != nil {
				continue
			}
			if resp := c.handle(msg.Cmd, msg.Data); resp != nil {
				resp.Cmd = msg.Cmd
				if b, mErr := json.Marshal(resp); mErr == nil {
					_ = conn.WriteMessage(websocket.TextMessage, append(b, '\n'))
				}
			}
		}
	}
}

// WsResp 行-JSON 响应：{cmd, data}。
type WsResp struct {
	Cmd  string      `json:"cmd"`
	Data interface{} `json:"data"`
}

// handle 按指令分发到 Store，返回响应体（nil 表示无需响应）。
func (c *WsComponent) handle(cmd string, data json.RawMessage) *WsResp {
	switch cmd {
	case "login":
		var req struct {
			Nickname string `json:"nickname"`
		}
		_ = json.Unmarshal(data, &req)
		id, best := c.store.Login("", req.Nickname)
		return &WsResp{Data: map[string]interface{}{"playerId": id, "best": best}}
	case "submitScore":
		var req struct {
			Nickname string `json:"nickname"`
			Score    int    `json:"score"`
			Win      bool   `json:"win"`
		}
		_ = json.Unmarshal(data, &req)
		rank, list := c.store.SubmitScore("", req.Nickname, req.Score, req.Win)
		return &WsResp{Data: map[string]interface{}{"rank": rank, "list": list}}
	case "getLeaderboard":
		return &WsResp{Data: map[string]interface{}{"list": c.store.GetLeaderboard()}}
	case "save":
		var req struct {
			PlayerID string          `json:"playerId"`
			Progress json.RawMessage `json:"progress"`
		}
		_ = json.Unmarshal(data, &req)
		c.store.Save(req.PlayerID, req.Progress)
		return &WsResp{Data: map[string]interface{}{}}
	case "load":
		var req struct {
			PlayerID string `json:"playerId"`
		}
		_ = json.Unmarshal(data, &req)
		return &WsResp{Data: map[string]interface{}{"progress": c.store.Load(req.PlayerID)}}
	default:
		return nil
	}
}
