# 最小 Windows M0

独立 amd64 Go 程序，使用锁定 OS 线程的消息循环、LL 键盘/鼠标钩子和 Raw Input。只输出计数，不打印键码、字符或鼠标坐标；正式客户端不导入本实验。

```sh
cmake --build --preset host --target windows-input-probe
```

Windows 运行 `build/host/bin/windows/inputprobe.exe`，默认 30 秒全部放行。日志中的键盘/按钮只来自 LL hook，相对移动和滚轮只来自 Raw Input。可分别测试：

```powershell
./build/host/bin/windows/inputprobe.exe -duration 15s
./build/host/bin/windows/inputprobe.exe -duration 15s -block-ms 1000
./build/host/bin/windows/inputprobe.exe -duration 15s -inject-f24
```

显式阻断实验等待 3 秒且物理键/按钮全部释放，最多阻断 2 秒；由独立计时路径恢复放行。记录钩子吞放时 Raw Input 计数是否继续，验证实际平台行为。F24 仅在显式 flag 下通过 SendInput 回注并附带自定义 marker，用 own-injected 计数检查排除路径。Ctrl+C 或计时结束会卸载钩子。

2026-11-02 已运行 15 秒默认放行探针，计数全为零；随后 F24 回注返回 Access is denied（0/2 成功），未运行阻断；[记录](results/2026-11-02.md) 不能作为输入行为通过的证据。完整性级别、安全桌面、会话锁定、崩溃恢复、ClipCursor、热键/多键盘聚合及 Linux 模式切换均未在此实验中完成验收。
