# M1 USB HID 硬件实验

固定 ESP-IDF 6.0.3、esp_tinyusb 3.0.0；这是独立测试应用。正式配网固件不自动敲字，测试应用不启动串口配网或网络服务。

在 WSL ext4 工作副本，先导出 IDF 环境，再使用根 CMake：

```sh
cmake --preset device
cmake --build --preset device
cmake --build --preset device --target rebuild-hid-selftest
cmake --build --preset device --target package-hid-selftest
```

VS Code 选择 `build` / `rebuild` 后，在第二级选择 **Device · HID 测试固件**。Windows 自动经 WSL 构建，无需 Windows CMake，也不必把工程重新打开为 WSL Remote。D 盘源码在 WSL ext4 镜像目录编译，烧录包自动复制回工作区 `build/device/flash/hid-selftest/<包目录>`，配置复制回 `build/device/config`。无需 Go、protoc、PSK 或 WiFi；构建任务不烧录。

按用户提供的设备日志，当前测试默认 VID=`0x046D`、PID=`0xC52B`。`MI_00` 是接口编号，不编码到 PID。可通过 IDF menuconfig 的 **HID hardware bring-up** 修改 VID/PID 和板上按钮 GPIO（默认 GPIO0）。已有 sdkconfig 会保留旧值；若此前配置为 0，需要在 menuconfig 中更新，值为 0 时应用仍会停止 USB 启动。

```sh
cd experiments/usb-hid
idf.py -B ../../build/device/hid-selftest menuconfig
```

配置保存在 `build/device/config/hid-selftest.sdkconfig`，与 IDF 二进制目录分开，`rebuild` 不丢失配置。USB 原生口连接受控机，UART 调试/烧录口连接 Windows。该实验允许 UART 日志，因为没有在同一 UART 启动配网协议。

枚举完成后先松开按钮；聚焦空白文本编辑器，然后按住 1–3 秒并松开按钮：固件按 USB 完成回调顺序输入 US 布局的 `espkvm`，小幅右移再左移鼠标，并向三个接口提交全释放。上电、复位或单纯插入 USB 不会自动敲字。

按住至少 3 秒后松开，固件先全释放，将相反的键盘模式写入 `hid_test/six_kro` NVS 并重启。模式为：

- 普通：键盘 Boot 子类，Report 模式 33B NKRO，Boot 模式 8B；处理 SET_PROTOCOL/SET_IDLE/LED。
- 固定 6KRO：描述符和输入报告均为标准 8B，主机不发 SET_PROTOCOL 也保持一致。超过 6 个普通键输出 ErrorRollOver。

鼠标独立 HID 接口：5 键、16 位 X/Y、8 位纵/横滚轮；Consumer 独立接口。默认实验不按媒体功能键，Consumer 的非零 usage 交付仍需后续实机用例。

单元测试覆盖报告长度、修饰键、保留位、6 键溢出及有符号鼠标字节。编译、bin 生成和回调等待不证明目标机收到字符。接板后需要填写：设备/BIOS 型号、系统/布局、两种模式枚举、字符/点按、LED、滚轮、Consumer、拔插、挂起恢复、SET_PROTOCOL 和 SET_IDLE 的观察结果。

该实验仅有一个应用生产者并同步等待完成，没有实现正式会话的 128 FIFO、鼠标分段累加或跨任务释放屏障；这些在 M1 实机路径确认后接入。
