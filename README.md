<div align="center">

# NetShunt

网卡角色化网络分流管理工具

为多网卡环境提供可视化的策略路由管理，将指定流量按 IP/域名 CIDR 引导到指定网卡出口，同时保持默认路由畅通。

[![license](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![platform](https://img.shields.io/badge/platform-Windows-green.svg)]()
[![version](https://img.shields.io/badge/version-3.1-orange.svg)]()

</div>

## 截图

| 首页 | 策略路由 | 诊断工具 |
|:---:|:---:|:---:|
| ![](screenshots/home.png) | ![](screenshots/strategy.png) | ![](screenshots/diagnostic.png) |

## 功能

- **策略路由管理** — 创建/编辑/删除分流策略，按 CIDR 规则将流量绑定到指定网卡
- **Fallback 默认出口** — 自动维护默认路由，确保非分流流量正常通行
- **自愈守护** — 后台监控路由表状态，异常时自动修复
- **安全重置** — 一键清理所有已注入路由，恢复系统默认路由表
- **流量监控** — 实时查看各网卡上下行流量
- **诊断工具** — 内置 Ping / Tracert / Nslookup / Netstat / 端口检测 / 测速
- **i18n 双语** — 中文 / English 一键切换
- **多主题** — 透明毛玻璃 / 紫色 / 苹果浅色
- **全局快捷键** — Ctrl+Alt+H 切换窗口显隐
- **系统托盘** — 最小化到托盘，网卡事件原生通知

## 安装

### 安装包（推荐）

下载 `NetShunt_3.0_Setup.exe`，双击安装即可。安装包已内置 WebView2 Runtime Bootstrapper，无需额外环境。

### 从源码构建

```bash
# 需要 Go 1.26+ 和 Wails CLI
go install github.com/wailsapp/wails/v2/cmd/wails@latest

git clone https://github.com/flecklesreeder-design/NetShunt.git
cd NetShunt/NetShunt-go

wails build -clean
# 产物在 build/bin/NetShunt-go.exe
```

## 使用

1. 以管理员权限运行
2. 在「网卡管理」页为每张网卡分配角色
3. 在「策略路由」页创建策略，添加 CIDR 地址段，绑定到目标网卡
4. 点击「应用」生效

## 技术栈

| 层 | 技术 |
|---|---|
| 后端 | Go 1.26 |
| 桌面框架 | Wails v2 |
| 前端 | HTML + CSS + JS |
| 渲染 | WebView2 |
| 安装包 | Inno Setup 6 |

## 项目结构

```
NetShunt-go/
├── app.go                    # API 桥接层
├── main.go                   # Wails 入口
├── internal/
│   ├── adapter/manager.go    # 网卡检测
│   ├── config/store.go       # 配置持久化
│   ├── models/models.go      # 数据模型
│   ├── routing/engine.go     # 路由引擎
│   └── utils/utils.go        # 工具函数
├── frontend/dist/            # 嵌入前端（go:embed）
└── build/windows/            # exe 图标 / manifest / 版本信息
```

## 支持

如果这个项目对你有帮助，欢迎请我喝杯咖啡 ☕

<div align="center">

| 微信支付 | 支付宝 |
|:---:|:---:|
| ![](screenshots/donate-wechat.jpg) | ![](screenshots/donate-alipay.jpg) |

</div>

也可提 Issue 或 Star ⭐ 支持。

## License

[MIT](LICENSE)

## Contact

924636096@qq.com
