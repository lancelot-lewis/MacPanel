package services

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/lancelot-lewis/MacPanel/internal/platform/execx"
)

// Controller 执行受管服务的启停。
//
// 安全边界（三层，缺一不可）：
//  1. 调用方必须已在受管白名单里登记该服务；
//  2. ValidateLabelForControl 拒绝系统关键服务与本应用自身；
//  3. 只操作 system 域，且只允许 kickstart / bootstrap / bootout 三个动作。
//
// 不提供「任意 plist 加载」：启动只能 bootstrap 该服务自己登记过的 plist，
// 路径由受管记录给出，不接受调用方传入。
type Controller struct {
	// run 执行 launchctl 命令（可注入用于测试）。
	run func(ctx context.Context, args ...string) error
	// stat 检查 plist 是否存在。
	stat func(path string) error
}

// NewController 创建真实控制器。
func NewController() *Controller {
	return &Controller{
		run: func(ctx context.Context, args ...string) error {
			// 启停可能需要等待服务状态切换，超时放宽到 30 秒。
			_, err := execx.Run(ctx, 30*time.Second, LaunchctlPath, args...)
			return err
		},
		stat: func(path string) error {
			_, err := os.Stat(path)
			return err
		},
	}
}

// NewControllerForTest 创建可注入依赖的控制器（仅供测试）。
func NewControllerForTest(run func(context.Context, ...string) error, stat func(string) error) *Controller {
	return &Controller{run: run, stat: stat}
}

// Action 是可执行的操作。
type Action string

const (
	// ActionStart 启动服务。对已运行的服务是幂等的（不重启）。
	ActionStart Action = "start"
	// ActionStop 停止服务（bootout：卸载并终止）。
	ActionStop Action = "stop"
	// ActionRestart 重启服务（kickstart -k）。
	ActionRestart Action = "restart"
)

// Valid 判断动作名是否合法。
func (a Action) Valid() bool {
	switch a {
	case ActionStart, ActionStop, ActionRestart:
		return true
	default:
		return false
	}
}

// Execute 对受管服务执行操作。
//
// plistPath 必须来自受管记录（数据库里的登记值），不接受 HTTP 调用方传入。
// 这一条是「不提供任意 plist 加载」的实现方式。
func (c *Controller) Execute(
	ctx context.Context, label string, action Action, plistPath string,
) error {
	if err := ValidateLabelForControl(label); err != nil {
		return err
	}
	if !action.Valid() {
		return fmt.Errorf("未知操作 %q", action)
	}

	switch action {
	case ActionRestart:
		// kickstart -k 会先终止再启动，不需要 plist 路径。
		// 对未运行的服务也会启动起来——这符合「重启」的直觉。
		return c.run(ctx, "kickstart", "-k", "system/"+label)

	case ActionStart:
		// 已加载时不重复 bootstrap：那会因「服务已存在」失败。
		// 直接返回成功更符合「启动」的幂等语义——用户点两次不该报错。
		if c.isLoaded(ctx, label) {
			return nil
		}
		if plistPath == "" {
			return fmt.Errorf("无法启动 %s：未登记 plist 路径", label)
		}
		if err := c.stat(plistPath); err != nil {
			return fmt.Errorf("无法启动 %s：plist 不存在（%s）", label, plistPath)
		}
		return c.run(ctx, "bootstrap", "system", plistPath)

	case ActionStop:
		// bootout 对未加载的服务会报错。未加载即已是「停止」状态，
		// 直接返回成功（幂等）。
		if !c.isLoaded(ctx, label) {
			return nil
		}
		return c.run(ctx, "bootout", "system/"+label)
	}
	return nil
}

// isLoaded 判断服务是否已加载（print 成功即已加载）。
func (c *Controller) isLoaded(ctx context.Context, label string) bool {
	// print 成功返回 nil；服务不存在时返回错误。
	return c.run(ctx, "print", "system/"+label) == nil
}
