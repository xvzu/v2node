# v2node

基于修改版 [xray-core](https://github.com/wyx2685/xray-core) 的 V2Board 节点服务端，支持多种代理协议。

**注意： 本项目需要搭配 [修改版 V2Board](https://github.com/wyx2685/v2board)**

## 软件安装

### 一键安装

```bash
wget -N https://raw.githubusercontent.com/xvzu/v2node/master/script/install.sh && bash install.sh
```

### 手动编译

需要 Go 1.26+（本项目的 `go.mod` 要求 `go 1.26.1`）

```bash
git clone https://github.com/xvzu/v2node.git
cd v2node
GOEXPERIMENT=jsonv2 go build -v -o v2node -trimpath \
  -ldflags "-X 'github.com/xvzu/v2node/cmd.version=$(git describe --tags 2>/dev/null || echo dev)' -s -w -buildid="
./v2node -c /etc/v2node/config.json
```

### 预编译版本

从 [Releases](https://github.com/xvzu/v2node/releases) 页面下载对应架构的二进制文件。

## 配置文件

```json
{
  "Log": {
    "Level": "info",
    "Output": "",
    "Access": "none"
  },
  "Nodes": [
    {
      "ApiHost": "https://your-panel.com",
      "NodeID": 1,
      "ApiKey": "your-api-key",
      "Timeout": 15,
      "RetryCount": 1
    }
  ],
  "PprofPort": 0
}
```

支持多节点配置，`Nodes` 数组可包含多个节点。配置文件支持热重载（`fsnotify` 监听，10 秒防抖）。

## 支持的协议

### 1. VLESS

| 项目 | 说明 |
|------|------|
| **传输层** | TCP / WebSocket / gRPC / HTTPUpgrade / SplitHTTP |
| **安全** | None / TLS / Reality |
| **认证** | UUID + Flow |
| **特点** | 支持 XTLS Vision 流控、MLKEM768 端到端加密 |

**V2Board 配置：**
- 协议：`vless`
- 服务端口：对应节点端口
- 传输方式：tcp / ws / grpc / httpupgrade / splithttp
- Flow 控制：`xtls-rprx-vision` / `xtls-rprx-vision-udp443` 等
- 底层传输安全：none / tls / reality
- 加密方式：可选 `mlkem768x25519plus`

---

### 2. VMess

| 项目 | 说明 |
|------|------|
| **传输层** | TCP / WebSocket / gRPC / HTTPUpgrade / SplitHTTP |
| **安全** | None / TLS / Reality |
| **认证** | UUID + Security("auto") |
| **特点** | 经典协议，兼容性好 |

**V2Board 配置：**
- 协议：`vmess`
- 服务端口：对应节点端口
- 传输方式：tcp / ws / grpc / httpupgrade / splithttp
- 底层传输安全：none / tls / reality

---

### 3. Trojan

| 项目 | 说明 |
|------|------|
| **传输层** | TCP / WebSocket / gRPC |
| **安全** | None / TLS / Reality |
| **认证** | UUID（作为密码） |

**V2Board 配置：**
- 协议：`trojan`
- 服务端口：对应节点端口
- 传输方式：tcp / ws / grpc
- 底层传输安全：none / tls / reality

---

### 4. Shadowsocks

| 项目 | 说明 |
|------|------|
| **传输层** | TCP（仅）+ 可选 HTTP 混淆 |
| **安全** | 仅 None（加密由 SS 自身处理） |
| **认证** | UUID（作为密码）+ Cipher |
| **特点** | 支持 SS2022 协议 |

**V2Board 配置：**
- 协议：`shadowsocks`
- 服务端口：对应节点端口
- 加密方式：`aes-128-gcm` / `aes-256-gcm` / `chacha20-poly1305` / `none`(plain)
- 传输方式：tcp（`network` 字段为空或 tcp）
- ServerKey（SS2022）：设置后将启用 SS2022 协议，加密方式需为 `2022-blake3-aes-128-gcm` / `2022-blake3-aes-256-gcm` / `2022-blake3-chacha20-poly1305`
- 混淆：在 `network_settings` 中设置 `path` 和 `host` 可启用 HTTP 混淆
- PROXY protocol：设置 `acceptProxyProtocol` 可接收 PROXY protocol 头

---

### 5. Hysteria2

| 项目 | 说明 |
|------|------|
| **传输层** | QUIC（Hysteria 协议） |
| **安全** | None / TLS / Reality |
| **认证** | UUID（作为 Auth 密码） |
| **特点** | 基于 QUIC，吞吐量大，支持 Brutal 拥塞控制 |

**V2Board 配置：**
- 协议：`hysteria2`
- 服务端口：对应节点端口
- 传输方式：Hysteria 默认（无需额外配置）
- 底层传输安全：none / tls / reality
- 客户端带宽限制：`UpMbps` / `DownMbps`（面板配置）
- 忽略客户端带宽：`Ignore_Client_Bandwidth`（面板配置）
- 混淆：`Obfs` + `ObfsPassword`
- 拥塞控制：默认 `brutal`

---

### 6. TUIC

| 项目 | 说明 |
|------|------|
| **传输层** | QUIC（TUIC 协议） |
| **安全** | None / TLS / Reality |
| **认证** | UUID（同时作为 UUID 和 Password） |
| **特点** | 基于 QUIC，延迟低 |

**V2Board 配置：**
- 协议：`tuic`
- 服务端口：对应节点端口
- 底层传输安全：none / tls / reality
- 拥塞控制：可选（面板 `CongestionControl`）
- 0-RTT 握手：可选（面板 `ZeroRttHandshake`）

---

### 7. AnyTLS

| 项目 | 说明 |
|------|------|
| **传输层** | TCP / WebSocket / gRPC / HTTPUpgrade / SplitHTTP |
| **安全** | None / TLS / Reality |
| **认证** | UUID（作为密码） |
| **特点** | 基于 TLS 的协议混淆，流量特征类似浏览器 TLS |

**V2Board 配置：**
- 协议：`anytls`
- 服务端口：对应节点端口
- 传输方式：tcp / ws / grpc / httpupgrade / splithttp
- 底层传输安全：none / tls / reality
- Padding Scheme：面板 `PaddingScheme`

---

### 8. Mieru

| 项目 | 说明 |
|------|------|
| **传输层** | 原始 TCP（自定义加密二进制协议） |
| **安全** | 自身加密（XChaCha20-Poly1305 + PBKDF2），不依赖 TLS |
| **认证** | UUID（双重 SHA256 哈希） |
| **架构** | **独立于 xray-core**，自建 TCP 代理服务器 |

Mieru 是一个**独立于 xray-core 运行的协议**。它不走 xray-core 的 inbound/dispatcher 体系，而是作为一个单独的 TCP 监听器运行在 v2node 进程中。

**加密流程：**
```
密码派生：SHA256(uuid + 0x00 + uuid) → hashedPwd
密钥派生：PBKDF2(hashedPwd, 时间盐值, 迭代次数, 32, SHA256) → 会话密钥
会话加密：XChaCha20-Poly1305 加密 tunnel payload
```

**V2Board 配置：**
- 协议：`mieru`
- 服务端口：对应节点端口
- 底层传输安全：none（加密由 Mieru 自身处理）

**客户端配置：**

下面是官方 mieru 客户端的配置示例：

```json
{
  "servers": [
    {
      "host": "your-server-ip",
      "port": 443,
      "users": [
        {
          "name": "your-uuid",
          "password": "your-uuid"
        }
      ],
      "shakeBinRoot": "./",
      "muxTunnelService": false,
      "multiplexTunnelService": false
    }
  ]
}
```

> 注意：用户名和密码都填用户在 V2Board 上分配的 UUID。

**目前限制：**
- 不支持设备/IP 限速（limiter 尚未适配）
- 不支持 speed limit
- 流量统计正常上报

---

## 协议特性对比

| 协议 | 基于 xray | 传输层 | 加密层 | SS2022 | Reality | Mux | UDP |
|------|-----------|--------|--------|--------|---------|-----|-----|
| **VLESS** | ✅ | tcp/ws/grpc/upgrade/splithttp | TLS/Reality/None | ❌ | ✅ | ✅ | ✅ |
| **VMess** | ✅ | tcp/ws/grpc/upgrade/splithttp | TLS/Reality/None | ❌ | ✅ | ✅ | ✅ |
| **Trojan** | ✅ | tcp/ws/grpc | TLS/Reality/None | ❌ | ✅ | ✅ | ✅ |
| **Shadowsocks** | ✅ | tcp | 自身 AEAD | ✅ | ❌ | ✅ | ✅ |
| **Hysteria2** | ✅ | QUIC | TLS/Reality/None | ❌ | ✅ | ✅ | ✅ |
| **TUIC** | ✅ | QUIC | TLS/Reality/None | ❌ | ✅ | ✅ | ✅ |
| **AnyTLS** | ✅ | tcp/ws/grpc/upgrade/splithttp | TLS/Reality/None | ❌ | ✅ | ✅ | ✅ |
| **Mieru** | ❌ | 原始 TCP | XChaCha20-Poly1305 | ❌ | ❌ | ❌ | ❌ |

## 构建

```bash
GOEXPERIMENT=jsonv2 go build -v -o v2node -trimpath \
  -ldflags "-X 'github.com/xvzu/v2node/cmd.version=$(git describe --tags 2>/dev/null || echo dev)' -s -w -buildid="
```

## 许可证

本项目原基于 MPL-2.0 许可证，自 v0.4.2 起变更为 **GPL-3.0**（因 mieru 库使用 GPL-3.0 许可证）。
