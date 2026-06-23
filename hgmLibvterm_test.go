package hgmLibvterm

import (
	"fmt"
	"strings"
	"testing"
)

// renderRow 把第 row 行还原成字符串: 宽字符写 1 个 rune 前进 2 列, 跳过续接格; 空格子补空格.
func renderRow(t *testing.T, scr *Screen, row, cols int) string {
	t.Helper()
	var sb strings.Builder
	for col := 0; col < cols; {
		cell, err := scr.GetCellAt(row, col)
		if err != nil {
			t.Fatalf("GetCellAt(%d,%d): %v", row, col, err)
		}
		w := cell.Width()
		if w < 1 {
			w = 1
		}
		chars := cell.Chars()
		if len(chars) == 0 {
			for k := 0; k < w; k++ {
				sb.WriteByte(' ')
			}
		} else {
			for _, r := range chars {
				sb.WriteRune(r)
			}
		}
		col += w
	}
	return strings.TrimRight(sb.String(), " ")
}

func newTestTerm(t *testing.T, rows, cols int) (*VTerm, *Screen, *int) {
	t.Helper()
	vt := New(NewReq_t{Rows: rows, Cols: cols}) // 默认开 UTF-8 + 硬 reset
	scr := vt.ObtainScreen()
	damageCount := 0
	scr.OnDamage = func(*Rect) int { damageCount++; return 1 }
	return vt, scr, &damageCount
}

// TestBasicRender: 普通 ASCII 写入 + damage 回调触发.
func TestBasicRender(t *testing.T) {
	vt, scr, dmg := newTestTerm(t, 4, 20)
	defer vt.Close()

	vt.Write([]byte("hello world"))
	scr.Flush()
	if *dmg == 0 {
		t.Fatalf("expected damage callback to fire on text write")
	}
	if got := renderRow(t, scr, 0, 20); got != "hello world" {
		t.Fatalf("row0 = %q, want %q", got, "hello world")
	}
}

// TestWideChar: CJK 宽字符 Width=2, Chars 返回 1 个 rune, 右邻为续接格. 这是手写解析器漏掉的.
func TestWideChar(t *testing.T) {
	vt, scr, _ := newTestTerm(t, 3, 20)
	defer vt.Close()

	vt.Write([]byte("a世b"))
	scr.Flush()

	c0, _ := scr.GetCellAt(0, 0)
	c1, _ := scr.GetCellAt(0, 1)
	c3, _ := scr.GetCellAt(0, 3)
	if c0.Width() != 1 || string(c0.Chars()) != "a" {
		t.Fatalf("cell0 width=%d chars=%q, want 1 'a'", c0.Width(), string(c0.Chars()))
	}
	if c1.Width() != 2 || string(c1.Chars()) != "世" {
		t.Fatalf("cell1 width=%d chars=%q, want 2 '世'", c1.Width(), string(c1.Chars()))
	}
	// col 2 是 世 的续接格; col 3 应该是 'b'.
	if c3.Width() != 1 || string(c3.Chars()) != "b" {
		t.Fatalf("cell3 width=%d chars=%q, want 1 'b'", c3.Width(), string(c3.Chars()))
	}
	if got := renderRow(t, scr, 0, 20); got != "a世b" {
		t.Fatalf("row0 = %q, want %q", got, "a世b")
	}
}

// TestColorEscapeIgnored: 纯颜色 escape 不该留下可见字符.
func TestColorEscapeIgnored(t *testing.T) {
	vt, scr, _ := newTestTerm(t, 3, 20)
	defer vt.Close()

	vt.Write([]byte("\x1b[31mRED\x1b[0m"))
	scr.Flush()
	if got := renderRow(t, scr, 0, 20); got != "RED" {
		t.Fatalf("row0 = %q, want %q (color escape must not print)", got, "RED")
	}
}

// TestSplitEscapeAcrossWrites 是内嵌这个库的核心理由: 一条转义序列被切成两次 Write 喂进来,
// libvterm 的有状态 parser 跨调用拼接, 续接字节不会被当正文打印.
// 手写解析器按固定字节块切, 这种情况会把 "1;5H" 之类的残段打印成乱码.
func TestSplitEscapeAcrossWrites(t *testing.T) {
	vt, scr, _ := newTestTerm(t, 5, 30)
	defer vt.Close()

	// 目标: 写 'A', 然后用 CSI 光标定位到 (row3,col5) 写 'B'. 但把这条 CSI "\x1b[3;6H"
	// 拦腰切成三段分次 Write, 模拟 PTY read 边界落在转义序列中间.
	vt.Write([]byte("A"))
	vt.Write([]byte("\x1b[3")) // CSI 半截
	vt.Write([]byte(";6"))     // 参数续接 (前一次没有 ESC 前缀)
	vt.Write([]byte("H"))      // final byte, 此处才真正定位
	vt.Write([]byte("B"))
	scr.Flush()

	if got := renderRow(t, scr, 0, 30); got != "A" {
		t.Fatalf("row0 = %q, want %q (split CSI must not leak its bytes as text)", got, "A")
	}
	// CSI 3;6H = 1-based (row3,col6) -> 0-based (row2,col5).
	if got := renderRow(t, scr, 2, 30); got != "     B" {
		t.Fatalf("row2 = %q, want 5 spaces then B (cursor positioned by reassembled CSI)", got)
	}
}

// TestSplitUtf8AcrossWrites: 多字节 UTF-8 字符被切在两次 Write 之间也要正确还原.
func TestSplitUtf8AcrossWrites(t *testing.T) {
	vt, scr, _ := newTestTerm(t, 3, 20)
	defer vt.Close()

	full := []byte("世") // 3 字节: e4 b8 96
	vt.Write(full[:1])
	vt.Write(full[1:])
	scr.Flush()

	c0, _ := scr.GetCellAt(0, 0)
	if string(c0.Chars()) != "世" {
		t.Fatalf("cell0 chars=%q, want '世' (split UTF-8 must reassemble)", string(c0.Chars()))
	}
}

// TestSplitUtf8AfterAsciiAcrossWrites 复现 libvterm 的一个真实 bug (2026-06-23 实测):
// 一个多字节 UTF-8 字符被切在两次 Write 之间, 且**第一次 Write 以 ASCII 字节开头**时,
// 字符会被解码成 U+FFFD 乱码 —— 而上面 TestSplitUtf8AcrossWrites (第一次 Write 直接是高位
// 首字节) 却正确. 区别只有第一次 Write 开头多了个 'A'.
//
// 根因 (state.c on_text 编码选择): libvterm 在 UTF-8 模式下有**两个独立的 UTF-8 解码器实例**
// (state->encoding[gl_set] 与 state->encoding_utf8), 各自带半截序列状态 (bytes_remaining/this_cp).
// on_text 每次按"本次调用首字节最高位"二选一:
//
//	!(bytes[eaten] & 0x80) ? &state->encoding[state->gl_set]   // 首字节 0x00-0x7f → GL 实例
//	state->vt->mode.utf8   ? &state->encoding_utf8 ...          // 首字节 0x80+   → utf8 实例
//
// 第一次 Write "A\xe4": 首字节 'A' 是 ASCII → 整段走 GL 实例, 把半截 lead byte \xe4 的状态
// (bytes_remaining=2) 存进 **GL 实例**. 第二次 Write "\xb8\x96": 首字节 \xb8 是高位续接字节 →
// 路由到 **encoding_utf8 实例** (它 bytes_remaining=0) → 两个续接字节都成了 orphan continuation
// → 各吐一个 U+FFFD. 半个字符存在这个实例、另一半喂给那个实例, 永远拼不上.
//
// 现实触发: claude CLI 的 TUI 输出经 PTY 每次最多 read 1024 字节, 像 " (decideDegradedViaEngine…"
// 这种 "ASCII 文本 + 行尾省略号/CJK" 的行尾正好被 1024 边界切在多字节字符中间 → 终端历史里出现 �.
//
// 本测试断言**正确行为** (应还原成 'A世'), 在修复前会 FAIL —— 这就是 bug 的复现.
// 可能修法: on_text 在 mode.utf8 时统一只用 encoding_utf8, 不按首字节高位在 gl_set/utf8 间切.
func TestSplitUtf8AfterAsciiAcrossWrites(t *testing.T) {
	vt, scr, _ := newTestTerm(t, 3, 20)
	defer vt.Close()

	// "A世" = 41 e4 b8 96. 切点落在 世 (e4 b8 96) 中间, 且第一次 Write 以 ASCII 'A' 开头.
	vt.Write([]byte("A\xe4")) // ASCII 'A' + 世 的首字节 (lead)
	vt.Write([]byte("\xb8\x96")) // 世 剩下的两个续接字节
	scr.Flush()

	c0, _ := scr.GetCellAt(0, 0)
	c1, _ := scr.GetCellAt(0, 1)
	if string(c0.Chars()) != "A" {
		t.Fatalf("cell0 chars=%q, want 'A'", string(c0.Chars()))
	}
	if string(c1.Chars()) != "世" {
		t.Fatalf("cell1 chars=%q, want '世' (split UTF-8 after ASCII must reassemble; "+
			"got U+FFFD => libvterm 双解码器实例 bug)", string(c1.Chars()))
	}
}

// TestScrollAndClear: 超出行数触发滚屏, CSI 2J 清屏由 libvterm 内部处理.
func TestScrollAndClear(t *testing.T) {
	vt, scr, _ := newTestTerm(t, 3, 10)
	defer vt.Close()

	vt.Write([]byte("L1\r\nL2\r\nL3\r\nL4")) // 4 行进 3 行屏幕 -> L1 滚出
	scr.Flush()
	if got := renderRow(t, scr, 0, 10); got != "L2" {
		t.Fatalf("after scroll row0 = %q, want L2", got)
	}

	vt.Write([]byte("\x1b[2J")) // 清屏
	scr.Flush()
	if got := renderRow(t, scr, 0, 10); got != "" {
		t.Fatalf("after clear row0 = %q, want empty", got)
	}
}

// renderCells 把 sb_pushline 回传的一行 cells 还原成字符串 (逻辑同 renderRow, 但输入是
// OnSbPushLine 的 cells 切片而非 GetCellAt). 行尾空格裁掉.
func renderCells(cells []ScreenCell) string {
	var sb strings.Builder
	for i := 0; i < len(cells); {
		c := cells[i]
		w := c.Width()
		if w < 1 {
			w = 1
		}
		chars := c.Chars()
		if len(chars) == 0 {
			sb.WriteByte(' ')
		} else {
			for _, r := range chars {
				sb.WriteRune(r)
			}
		}
		i += w
	}
	return strings.TrimRight(sb.String(), " ")
}

// TestSbPushLine: 行从主屏顶部滚出时 OnSbPushLine 应按顺序拿到滚出的整行内容.
// 这是采集 scrollback ("被挤到上面的历史") 的关键路径.
func TestSbPushLine(t *testing.T) {
	vt, scr, _ := newTestTerm(t, 3, 10)
	defer vt.Close()

	var scrollback []string
	scr.OnSbPushLine = func(cells []ScreenCell) int {
		scrollback = append(scrollback, renderCells(cells))
		return 0
	}

	// 6 行进 3 行屏幕 -> L1 L2 L3 依次滚出顶部, 屏幕只剩 L4 L5 L6.
	vt.Write([]byte("L1\r\nL2\r\nL3\r\nL4\r\nL5\r\nL6"))
	scr.Flush()

	want := []string{"L1", "L2", "L3"}
	if len(scrollback) != len(want) {
		t.Fatalf("scrollback = %q, want %q", scrollback, want)
	}
	for i := range want {
		if scrollback[i] != want[i] {
			t.Fatalf("scrollback[%d] = %q, want %q (full=%q)", i, scrollback[i], want[i], scrollback)
		}
	}
	// 可见屏应只剩末 3 行.
	if got := renderRow(t, scr, 0, 10); got != "L4" {
		t.Fatalf("visible row0 = %q, want L4", got)
	}
}

// TestSbPushLineAltScreenScrollRegion: 新版 claude/codex TUI 跑在 alt-screen(DECSET ?1049h)
// 且用 scroll-region(DECSTBM) —— 滚出区顶的行原本被 upstream libvterm 丢弃 (gate 在 primary +
// dest.start_row==0). 放宽后这种场景也应能 push 出滚出去的行, 否则详情页"完整终端历史"只剩最后一屏.
func TestSbPushLineAltScreenScrollRegion(t *testing.T) {
	vt, scr, _ := newTestTerm(t, 5, 10)
	defer vt.Close()

	var scrollback []string
	scr.OnSbPushLine = func(cells []ScreenCell) int {
		scrollback = append(scrollback, renderCells(cells))
		return 0
	}

	// 进 alt screen; 设滚动区 rows 2..4 (1-based, 0-based 1..3); 光标移到区顶 (row2,col1).
	// 然后在区内写 A B C D E, 触发区顶滚出: A B 应被 push, 可见区剩 C D E.
	vt.Write([]byte("\x1b[?1049h\x1b[2;4r\x1b[2;1H"))
	vt.Write([]byte("A\r\nB\r\nC\r\nD\r\nE"))
	scr.Flush()

	want := []string{"A", "B"}
	if len(scrollback) != len(want) {
		t.Fatalf("alt-screen scroll-region scrollback = %q, want %q", scrollback, want)
	}
	for i := range want {
		if scrollback[i] != want[i] {
			t.Fatalf("scrollback[%d] = %q, want %q (full=%q)", i, scrollback[i], want[i], scrollback)
		}
	}
	// 滚动区现在 (0-based) row1=C row2=D row3=E; 区外 row0 仍空.
	if got := renderRow(t, scr, 1, 10); got != "C" {
		t.Fatalf("region top row1 = %q, want C", got)
	}
	if got := renderRow(t, scr, 3, 10); got != "E" {
		t.Fatalf("region bottom row3 = %q, want E", got)
	}
}

// TestSbPushLineNilSafe: 不设 OnSbPushLine 时滚动不 panic (导出回调判 nil 直接返回),
// 保证加该字段对老调用方零影响.
func TestSbPushLineNilSafe(t *testing.T) {
	vt, scr, _ := newTestTerm(t, 2, 6)
	defer vt.Close()
	vt.Write([]byte("a\r\nb\r\nc\r\nd")) // 滚动多次, OnSbPushLine 为 nil
	scr.Flush()
	if got := renderRow(t, scr, 0, 6); got != "c" {
		t.Fatalf("visible row0 = %q, want c", got)
	}
}

// TestEmptyWrite: 空切片 Write 走早返回分支, 返回 0 且不 panic.
func TestEmptyWrite(t *testing.T) {
	vt := New(NewReq_t{Rows: 2, Cols: 4})
	defer vt.Close()

	n, err := vt.Write(nil)
	if n != 0 || err != nil {
		t.Fatalf("Write(nil) = (%d,%v), want (0,nil)", n, err)
	}
	n, err = vt.Write([]byte{})
	if n != 0 || err != nil {
		t.Fatalf("Write([]) = (%d,%v), want (0,nil)", n, err)
	}
}

// TestDoubleClose: Close 多次调用安全 (第二次走 term==nil 早返回分支).
func TestDoubleClose(t *testing.T) {
	vt := New(NewReq_t{Rows: 2, Cols: 4})
	if err := vt.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := vt.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestGetCellOutOfRange: 越界坐标返回 error.
func TestGetCellOutOfRange(t *testing.T) {
	vt := New(NewReq_t{Rows: 3, Cols: 5})
	defer vt.Close()
	scr := vt.ObtainScreen()

	if _, err := scr.GetCellAt(100, 100); err == nil {
		t.Fatalf("GetCellAt(100,100) err = nil, want out-of-range error")
	}
}

// TestNewReqDisableOptions: DisableUTF8 + DisableReset 两个分支, 以及 SetUTF8(false)/Reset(false).
func TestNewReqDisableOptions(t *testing.T) {
	vt := New(NewReq_t{Rows: 2, Cols: 8, DisableUTF8: true, DisableReset: true})
	defer vt.Close()
	scr := vt.ObtainScreen()

	// 未禁用 UTF-8 的默认行为已被其他用例覆盖; 这里手动切两个 bool 分支.
	vt.SetUTF8(false)
	vt.SetUTF8(true)
	scr.Reset(false) // 软重置分支
	scr.Reset(true)

	vt.Write([]byte("ok"))
	scr.Flush()
	if got := renderRow(t, scr, 0, 8); got != "ok" {
		t.Fatalf("row0 = %q, want %q", got, "ok")
	}
}

// TestRectAccessors: damage 回调拿到的 Rect 四个坐标访问器都能读到合理值.
func TestRectAccessors(t *testing.T) {
	vt := New(NewReq_t{Rows: 4, Cols: 10})
	defer vt.Close()
	scr := vt.ObtainScreen()

	var got *Rect
	scr.OnDamage = func(r *Rect) int { got = r; return 1 }
	vt.Write([]byte("hi"))
	scr.Flush()

	if got == nil {
		t.Fatalf("OnDamage did not fire")
	}
	if got.StartRow() < 0 || got.EndRow() <= got.StartRow() {
		t.Fatalf("rows: start=%d end=%d", got.StartRow(), got.EndRow())
	}
	if got.StartCol() < 0 || got.EndCol() <= got.StartCol() {
		t.Fatalf("cols: start=%d end=%d", got.StartCol(), got.EndCol())
	}
}

// TestCombiningChars: Chars 返回基础字符 + 组合字符 (多于 1 个 rune).
func TestCombiningChars(t *testing.T) {
	vt, scr, _ := newTestTerm(t, 2, 8)
	defer vt.Close()

	// 'e' + U+0301 (组合尖音符) 落在同一格.
	vt.Write([]byte("é"))
	scr.Flush()
	c0, _ := scr.GetCellAt(0, 0)
	if string(c0.Chars()) != "é" {
		t.Fatalf("cell0 chars=%q, want %q (base + combining)", string(c0.Chars()), "é")
	}
}

// Example 既是文档示例也是可运行用例: 建终端 -> 写带颜色和宽字符的字节 -> 整屏读回第 0 行.
func Example() {
	vt := New(NewReq_t{Rows: 4, Cols: 20}) // 默认开 UTF-8 + 硬 reset
	defer vt.Close()
	scr := vt.ObtainScreen()

	changed := false
	scr.OnDamage = func(*Rect) int { changed = true; return 1 }

	// 红色 "hi", 空格, 宽字符 "世界", 再复位颜色. 颜色 escape 不会留下可见字符.
	vt.Write([]byte("\x1b[31mhi 世界\x1b[0m"))
	scr.Flush() // 同步触发 OnDamage

	var sb strings.Builder
	for col := 0; col < 20; {
		cell, err := scr.GetCellAt(0, col)
		if err != nil {
			break
		}
		w := cell.Width()
		if w < 1 {
			w = 1
		}
		chars := cell.Chars()
		if len(chars) == 0 {
			for k := 0; k < w; k++ {
				sb.WriteByte(' ')
			}
		} else {
			for _, r := range chars {
				sb.WriteRune(r)
			}
		}
		col += w
	}
	fmt.Println(strings.TrimRight(sb.String(), " "))
	fmt.Println("changed:", changed)
	// Output:
	// hi 世界
	// changed: true
}
