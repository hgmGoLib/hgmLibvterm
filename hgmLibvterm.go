// Package hgmLibvterm 是 libvterm (C, MIT, leonerd) 0.3.3 的最小 cgo 绑定, 连同 libvterm
// 的 C 源码一起内嵌在本目录, 由 cgo 直接编译 (同目录 .c 文件 cgo 会自动编进来).
//
// 它把一段终端字节流还原成"真实屏幕": New 建终端 -> Write 喂字节 -> Flush -> GetCellAt
// 逐格读出屏幕内容. 典型用途是读取跑在 PTY 里的全屏 TUI 程序 (例如 claude / codex CLI)
// 的当前屏幕. 用法和注意事项见 readme.txt.
//
// 为什么内嵌而不是用系统库:
//   brew 装的 libvterm 同时给 .a 和 .dylib, macOS ld 默认挑 .dylib, 编出来的二进制带运行期
//   /opt/homebrew/.../libvterm.0.dylib 依赖, 换机器/CI 就跑不起来, 还得靠 PKG_CONFIG_PATH
//   symlink hack 才能编译. 内嵌后: 无 .a / 无 .dylib / 无 pkg-config / 无 brew, 二进制完全
//   自包含. 仍需 CGO_ENABLED=1 (clang 或 /opt/zig/zig 都行).
//
// 用户指针走 runtime/cgo.Handle (Go 1.17+), 不引入任何第三方 Go 依赖.
package hgmLibvterm

/*
#cgo CFLAGS: -I${SRCDIR} -I${SRCDIR}/include
#include <vterm.h>
#include <stdint.h>

// damage 回调由 Go 实现 (见下方 //export). 其余回调置 NULL.
extern int _go_vt_damage(VTermRect rect, void *user);

static VTermScreenCallbacks _hgm_screen_cbs = {
	.damage = _go_vt_damage,
};

// _hgm_set_damage_cb 把 cgo.Handle (uintptr) 当不透明 void* 存进 libvterm,
// damage 触发时原样回传给 _go_vt_damage.
static void _hgm_set_damage_cb(VTermScreen *screen, uintptr_t user) {
	vterm_screen_set_callbacks(screen, &_hgm_screen_cbs, (void*)user);
}
*/
import "C"
import (
	"errors"
	"runtime/cgo"
	"unsafe"
)

// VTerm 是一个 libvterm 终端实例.
type VTerm struct {
	term   *C.VTerm
	screen *Screen
	handle cgo.Handle
}

// Screen 是 VTerm 的屏幕视图.
type Screen struct {
	screen *C.VTermScreen
	// OnDamage 在 Flush() 内同步回调 (与调用 Flush 的 goroutine 同线程), 返回值传回 C (一般忽略).
	OnDamage func(*Rect) int
}

// Rect 是一块屏幕矩形区域 (damage 回调参数). 当前调用方只把它当不透明标志位用,
// 但保留坐标访问以备后用.
type Rect struct {
	rect C.VTermRect
}

func (r *Rect) StartRow() int { return int(r.rect.start_row) }
func (r *Rect) EndRow() int   { return int(r.rect.end_row) }
func (r *Rect) StartCol() int { return int(r.rect.start_col) }
func (r *Rect) EndCol() int   { return int(r.rect.end_col) }

// ScreenCell 是屏幕上一个格子的内容.
type ScreenCell struct {
	cell C.VTermScreenCell
}

// NewReq_t 是 New 的参数. Rows/Cols 是终端的行/列数 (注意: 行在前).
// 默认行为: 开启 UTF-8 解码 + 硬重置到空屏; 两个 Disable 字段可分别跳过.
type NewReq_t struct {
	Rows int
	Cols int
	// DisableUTF8 为 true 时不开 UTF-8 解码 (默认 false = 开启). 现代 TUI 输出基本都是 UTF-8.
	DisableUTF8 bool
	// DisableReset 为 true 时不做初始硬重置 (默认 false = 硬重置到初始空屏).
	DisableReset bool
}

// New 按 req 新建一个终端. 用完必须 Close 释放 C 资源 + cgo.Handle.
func New(req NewReq_t) *VTerm {
	term := C.vterm_new(C.int(req.Rows), C.int(req.Cols))
	scr := &Screen{screen: C.vterm_obtain_screen(term)}
	vt := &VTerm{term: term, screen: scr}
	vt.handle = cgo.NewHandle(scr)
	C._hgm_set_damage_cb(scr.screen, C.uintptr_t(vt.handle))
	if !req.DisableUTF8 {
		vt.SetUTF8(true)
	}
	if !req.DisableReset {
		scr.Reset(true)
	}
	return vt
}

// Close 释放 libvterm 的 C 内存并删除 cgo.Handle. 多次调用安全.
func (vt *VTerm) Close() error {
	if vt.term == nil {
		return nil
	}
	C.vterm_free(vt.term)
	vt.term = nil
	vt.handle.Delete()
	return nil
}

// SetUTF8 决定输入字节是否按 UTF-8 解码. New 已按 NewReq_t.DisableUTF8 处理 (默认开启),
// 一般无需手动调用.
func (vt *VTerm) SetUTF8(b bool) {
	var v C.int
	if b {
		v = 1
	}
	C.vterm_set_utf8(vt.term, v)
}

// Write 把 PTY 字节喂进 parser. libvterm 的 parser 有状态: 跨调用保留半截转义序列/半个 UTF-8 字符,
// 这正是它取代手写 VT100 解析器的关键 (后者按块切, 半截序列会被吞或当正文打印).
func (vt *VTerm) Write(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	n := C.vterm_input_write(vt.term, (*C.char)(unsafe.Pointer(&b[0])), C.size_t(len(b)))
	return int(n), nil
}

// ObtainScreen 返回屏幕视图 (与 New 时创建的同一个).
func (vt *VTerm) ObtainScreen() *Screen {
	return vt.screen
}

// Reset 重置屏幕. hard=true 为硬重置.
func (s *Screen) Reset(hard bool) {
	var v C.int
	if hard {
		v = 1
	}
	C.vterm_screen_reset(s.screen, v)
}

// Flush 把累积的 damage flush 出去, 期间同步触发 OnDamage 回调.
func (s *Screen) Flush() error {
	C.vterm_screen_flush_damage(s.screen)
	return nil
}

// GetCellAt 取 (row,col) 处的格子. 越界/失败返回 error.
func (s *Screen) GetCellAt(row, col int) (*ScreenCell, error) {
	var pos C.VTermPos
	pos.row = C.int(row)
	pos.col = C.int(col)
	var cell ScreenCell
	if C.vterm_screen_get_cell(s.screen, pos, &cell.cell) == 0 {
		return nil, errors.New("hgmLibvterm: GetCellAt out of range")
	}
	return &cell, nil
}

// Width 是该格子的显示宽度 (1 = 普通, 2 = 宽字符如 CJK/部分 emoji). 宽字符占两列,
// 它右边那一列是续接格 (width 0).
func (sc *ScreenCell) Width() int {
	return int(sc.cell.width)
}

// Chars 返回该格子的码点: chars[0] 是基础字符, 后续是组合字符 (accent 之类). 读到 0 即止.
// 注意这是"码点个数", 与 Width (显示宽度) 是两码事: 一个 CJK 字 Chars 返回 1 个 rune 但 Width=2.
func (sc *ScreenCell) Chars() []rune {
	out := make([]rune, 0, C.VTERM_MAX_CHARS_PER_CELL)
	for i := 0; i < int(C.VTERM_MAX_CHARS_PER_CELL); i++ {
		r := rune(sc.cell.chars[i])
		if r == 0 {
			break
		}
		out = append(out, r)
	}
	return out
}

//export _go_vt_damage
func _go_vt_damage(rect C.VTermRect, user unsafe.Pointer) C.int {
	scr, ok := cgo.Handle(uintptr(user)).Value().(*Screen)
	if !ok || scr.OnDamage == nil {
		return 0
	}
	return C.int(scr.OnDamage(&Rect{rect: rect}))
}
