// actor_wgjx 是《万国觉醒·统帅远征》后端在 cherry 中注册的业务 actor。
// 它持有一个共享的 Store 引用，负责：
//  1. 周期性（每 30s）将内存状态落盘；
//  2. 进程退出前最后一次落盘。
//
// actor 由 cherry 的 actor 系统托管（独立 goroutine + 串行消息队列），
// 这里的周期任务即其「真工作」，落盘调用本身由 Store 内部互斥锁保证并发安全。
package main

import (
	"time"

	clog "github.com/cherry-game/cherry/logger"
	cactor "github.com/cherry-game/cherry/net/actor"
)

// wgjxActor 业务 actor，actorID 为 "wgjx"。
type wgjxActor struct {
	cactor.Base
	store *Store
}

// AliasID 返回 actor 路径的第二段：节点id.wgjx。
func (p *wgjxActor) AliasID() string {
	return "wgjx"
}

// OnInit 在 actor 创建后调用一次：绑定共享 Store 并启动周期落盘定时器。
func (p *wgjxActor) OnInit() {
	p.store = theStore
	p.Timer().Add(30*time.Second, func() {
		if err := p.store.Persist(); err != nil {
			clog.Warnf("[wgjx] persist error: %v", err)
		}
	})
}

// OnStop 进程退出前最后一次落盘。
func (p *wgjxActor) OnStop() {
	if err := p.store.Persist(); err != nil {
		clog.Warnf("[wgjx] persist on stop error: %v", err)
	}
}
