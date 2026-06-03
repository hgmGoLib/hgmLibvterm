hgmLibvterm
===========
一个 Go 库: 把"一段终端输出的字节流"还原成"屏幕上最终长什么样"—— 逐格拿到每个字符
和它的显示宽度. 底层是内嵌的 libvterm (C 写的 headless 终端模拟器), 用 cgo 直接编进来,
不依赖系统库 (理由见文末).


能干啥
------
典型场景: 你用 PTY 跑一个全屏 TUI 程序 (比如 claude CLI / codex CLI 这类), 它会往 PTY
里吐一大堆带光标移动 / 颜色 / 清屏的转义序列. 你想知道"此刻屏幕上实际显示的文字是什么"
(拿去给 webui 展示, 或者靠它判断程序是不是还在动). 直接读原始字节读不出来 —— 里面混着
一堆 \x1b[... 控制码. 把字节喂给本库, 再 GetCellAt 逐格读, 就拿到还原后的屏幕内容.

注意: 本库只负责"字节流 -> 屏幕"这一段. 它不帮你起进程, 也不帮你分配 PTY. 你得自己用一个
伪终端 (PTY) 库去 fork 子进程、拿到它的输出字节, 再喂进来. PTY 推荐 github.com/creack/pty
(Go 社区最常用的那个).


为什么用这个库, 别自己写
------------------------
"不就是去掉 \x1b[...m 颜色码吗, 正则替换一下不就行了?" —— 这条路我们踩过坑:
  1. 转义序列不止颜色. 还有光标定位 (CSI H)、清屏 (CSI 2J)、滚动、保存/恢复光标……
     正则删不干净; 删错了正文就花了.
  2. PTY 是按块 read 的 (比如每次 4096 字节), 一条转义序列 / 一个多字节 UTF-8 字符很可能
     正好被切在两次 read 中间. 手写解析器按块处理时, 这半截序列要么被当正文打印成乱码
     (典型现象: 屏幕上冒出 "1;5H"、"esc to interrupt" 之类错位文字), 要么被整段吞掉.
  3. 宽字符 (CJK / 部分 emoji) 占两列, 算光标列号时必须知道这点, 否则后面整行错位.
libvterm 是一个有状态的解析器: 你按 read 到的块无脑 Write 进去, 半截序列它自己跨调用拼接,
宽字符它自己算宽度. 这些恰恰是自己写最容易写错的地方.


怎么用
------
import vterm "github.com/hgmGoLib/hgmLibvterm"

核心流程: New 建终端 -> (可选) 挂 OnDamage 回调 -> 把读到的字节 Write 进去 -> Flush 触发
回调 -> GetCellAt 逐格读出屏幕内容. 用完 Close.

    // 1. 建一个 rows x cols 的终端 (行在前, 列在后). 默认开 UTF-8 + 硬 reset 到空屏.
    vt := vterm.New(vterm.NewReq_t{Rows: 60, Cols: 220})
    defer vt.Close()           // 必须, 释放 C 内存 + cgo.Handle
    scr := vt.ObtainScreen()
    // 想关掉默认行为: vterm.NewReq_t{..., DisableUTF8: true, DisableReset: true}

    // 2. (可选) 挂 damage 回调: 哪一帧真改了格子就会被调到.
    //    回调在 scr.Flush() 内同步触发 (同一 goroutine), 一般只用来置个"屏幕变过"标志.
    changed := false
    scr.OnDamage = func(*vterm.Rect) int { changed = true; return 1 }

    // 3. 把字节喂进去. 关键: parser 有状态, 一条转义序列 / 一个多字节 UTF-8 字符被切成多次
    //    Write 也能正确拼接 —— 所以可以无脑按 read 到的块直接 Write, 不用自己攒边界.
    vt.Write(buf1)             // 比如 ptmx.Read 出来的块
    vt.Write(buf2)
    scr.Flush()                // 把累积的 damage flush 出来, 触发 OnDamage

    // 4. 逐格读屏. Width()=显示宽度(1/2), Chars()=该格码点(读到 0 即止).
    //    宽字符 Width=2 且占两列, 右邻是续接格 —— 渲染时写 1 个 rune 然后列号 +2 跳过续接格.
    //    空格子 Chars() 为空, 按 Width 补空格保持对齐.
    var sb strings.Builder
    for col := 0; col < 220; {
        cell, err := scr.GetCellAt(0, col) // (row, col), 0-based
        if err != nil { break }
        w := cell.Width(); if w < 1 { w = 1 }
        chars := cell.Chars()
        if len(chars) == 0 {
            for k := 0; k < w; k++ { sb.WriteByte(' ') }
        } else {
            for _, r := range chars { sb.WriteRune(r) }
        }
        col += w
    }
    line0 := strings.TrimRight(sb.String(), " ")

可直接运行的完整示例见 hgmLibvterm_test.go 里的 Example 和各 TestXxx.

并发: 一个 VTerm 不是线程安全的. Write / Flush / GetCellAt 要么都在同一 goroutine,
要么调用方自己加锁. Close 之后不要再碰它.

暴露的 API (按需最小集, 不是 libvterm 全功能):
    type NewReq_t struct { Rows, Cols int; DisableUTF8, DisableReset bool }
    func New(req NewReq_t) *VTerm
    (*VTerm) SetUTF8(b bool)                 // New 已按 NewReq_t 处理, 一般无需手动调
    (*VTerm) Write(b []byte) (int, error)    // parser 有状态, 跨调用拼接半截序列
    (*VTerm) ObtainScreen() *Screen
    (*VTerm) Close() error                   // 多次调用安全
    (*Screen) Reset(hard bool)
    (*Screen) Flush() error                  // 同步触发 OnDamage
    (*Screen) GetCellAt(row, col int) (*ScreenCell, error)
    Screen.OnDamage func(*Rect) int          // 字段, 可不设
    (*ScreenCell) Width() int                // 1 普通 / 2 宽字符
    (*ScreenCell) Chars() []rune             // 码点, 读到 0 止
    (*Rect) StartRow/EndRow/StartCol/EndCol() int
要别的 libvterm 功能 (颜色 / 属性 / 滚动区域回调等) 自己照着 include/vterm.h 往
hgmLibvterm.go 里加.


为什么内嵌 C 源码, 不用系统库
----------------------------
brew 装的 libvterm 同时给 libvterm.a 和 libvterm.0.dylib, macOS 的 ld 在 `-lvterm` 下
默认挑 .dylib, 编出来的二进制带运行期依赖 /opt/homebrew/.../libvterm.0.dylib, 换机器 / CI
就跑不起来, 而且编译还得靠 PKG_CONFIG_PATH symlink hack 才能让 pkg-config 找到 vterm.pc.
内嵌 C 源码后: 无 .a / 无 .dylib / 无 pkg-config / 无 brew, 二进制完全自包含.
(仍需 CGO_ENABLED=1, clang 或 /opt/zig/zig 都行.)


C 源码从哪来
------------
上游项目 libvterm (作者 Paul "LeoNerd" Evans), 协议 MIT (见本目录 LICENSE).
  官网 / tarball:   https://www.leonerd.org.uk/code/libvterm/
  版本:             0.3.3
  tarball:          https://www.leonerd.org.uk/code/libvterm/libvterm-0.3.3.tar.gz
  tarball sha256:   09156f43dd2128bd347cbeebe50d9a571d32c64e0cf18d211197946aff7226e0
  上游 VCS:         https://git.sr.ht/~leonerd/libvterm  (release tag v0.3.3)
                    (github 镜像: https://github.com/neovim/libvterm)

注意: 一定用 release tarball, 不要 git 裸仓 —— tarball 里 fullwidth.inc / encoding/*.inc
这些编码表是预生成好的; 裸仓要靠一个脚本现生成才有, 麻烦.

从 tarball 拷进来的文件 (其余 bin/ t/ Makefile 等都没要):
  src/*.c                  -> ./*.c            (cgo 编译这些)
  src/vterm_internal.h
  src/utf8.h src/rect.h    -> ./*.h
  src/fullwidth.inc        -> ./fullwidth.inc          (unicode.c include)
  src/encoding/*.inc       -> ./encoding/*.inc         (encoding.c include)
  include/vterm.h
  include/vterm_keycodes.h -> ./include/*.h            (-I${SRCDIR}/include)
  LICENSE                  -> ./LICENSE
C 源码本身一个字没改.

Go 绑定 (hgmLibvterm.go) 是按需自己写的最小版, 思路参考过 github.com/mattn/go-libvterm,
但只暴露用到的那点 API, 且用 runtime/cgo.Handle 取代了 go-pointer, 不引入任何第三方 Go 依赖.


怎么升级 libvterm
-----------------
1. 下新版 release tarball, 核对 sha256.
2. 按上面"拷进来的文件"清单覆盖对应文件 (C 源码不要手改).
3. 如果上游加了新的 .c / .inc, 对应补上.
4. 跑测试: go test ./hgmLibvterm/ -v


测试
----
go test ./hgmLibvterm/ -v
go test ./hgmLibvterm/ -cover       # 看 Go 代码覆盖率

(本目录是普通 package, 直接跑即可. 注意别放进名为 vendor 的目录 —— go 工具对 vendor
有特殊处理, 放那儿 `go test .` 会报 "has no package path".)
