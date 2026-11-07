// 万国觉醒·统帅远征 —— 后端服务入口。
//
// 技术栈：cherry（Go Actor 框架）作为应用骨架与生命周期容器，
// 注册一个业务 actor（wgjxActor）负责周期落盘，
// 并挂载自定义原始 WebSocket 连接器组件（ws_component）提供行-JSON 接口。
//
// 启动方式（务必在工程根目录，配置文件路径相对当前工作目录）：
//
//	go run .            # 监听 :9105，数据落盘到 ./wgjx_data.json
//
// 前端 wgjx.vue 通过 resolveUrl() 自动连 ws://<host>:9105；
// 后端不可达时前端自动降级为 localStorage 离线模式，游戏始终可玩。
package main

import (
	"os"

	cherry "github.com/cherry-game/cherry"
	cserializer "github.com/cherry-game/cherry/net/serializer"
)

const (
	dataFile   = "./wgjx_data.json"
	configPath = "./config/wgjx.json"
	staticDir  = "./public" // 前端 H5 构建产物（dist/build/h5）拷贝至此
)

// listenAddr 监听地址：可用 PORT 环境变量覆盖（默认 :9105），便于发布平台统一端口。
func listenAddr() string {
	if p := os.Getenv("PORT"); p != "" {
		return ":" + p
	}
	return ":9105"
}

// theStore 全局业务状态，由 actor 与 WS 组件共享（内部互斥锁保证并发安全）。
var theStore = NewStore(dataFile)

func main() {
	addr := listenAddr()
	app := cherry.Configure(configPath, "wgjx-1", false, cherry.Standalone)
	app.SetSerializer(cserializer.NewJSON())
	app.AddActors(&wgjxActor{})
	app.Register(NewWsComponent(theStore, addr, staticDir))
	app.Startup()
}
