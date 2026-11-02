# Linux 输入实验

默认 LOCAL 被动读取，不抓取、不创建 uinput，不输出键码或字符。显式抓取实验只保持最多两秒；进入前检查全释放，抓取全部设备后再次读取物理状态，失败全部撤销。信号、退出、热插拔和 SYN_DROPPED 均回 LOCAL。多个设备通过引用计数聚合物理状态。

```sh
cc -std=c11 -Wall -Wextra -Werror probe.c -o linux-input-probe
./linux-input-probe --duration 15
./linux-input-probe --duration 15 --grab-seconds 2
```

请在有真实 evdev 设备的 Linux 桌面执行，记录发行版、内核、桌面环境、权限、热插拔、双键盘同键、抓取失败回滚、退出恢复结果。WSL 无输入设备时的运行只验证诊断路径，不算实机验收。此实验不做 uinput 普通输入回注；自建 EspKVM 虚拟设备被排除。
