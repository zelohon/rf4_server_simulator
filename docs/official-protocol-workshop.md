# 官方服务器协议抓包结论（工坊修理/换件）

抓包方式：`tools/frida/net_capture.py`（客户端进程内只读 hook，未改任何网络数据）。
样本：`logs/net_capture_official.jsonl`、`logs/net_capture_official2.jsonl`，
解码工具：`go run ./tools/cmd/netdecode <jsonl> <cmd|call:N>`。

## 21/1 工坊列表（响应）

根数组 args code **144**，元素 `ookkpknacja`(505)：

| 字段 | 内容 |
|---|---|
| f0 | 物品 GUID |
| f1 | 完好度 f32（位形，观测 0.9991~1.0） |
| f2 | 固定 **4** |
| f3 | `oinpmjophkh[]`：**typed_u32arr**（0x07 + 类型名），无磨损时为空 |

> 注意：官方确实用 typed_u32arr。我们之前发 "17" 这个类型名导致
> `kkeibjcmjnl -> oinpmjophkh[]` 强转失败；应发空的 typed 数组或与官方一致的类型名。

## 21/2 工坊诊断/报价（响应）

`mgjeoebmghh`(503) `fcpoceohlbj`：

```
f0 = 物品 GUID
f1 = args(143) 零件数组，元素 hlopebmchhp(504)：
     f0 = 零件 GUID
     f1 = 完好度 f32（位形）
     f2 = 固定 4
     f3 = 价格对象 oifghenlghh(17497) {silver, gold}   ← 该零件的修理报价
     f4 = BYTE 0
     f5 = args(146) 可更换件列表，元素 hpopgbjohpk(507)：
          f0 = STR  替代件 systemID（如 "bearings_5719"）
          f1 = 价格对象 {silver, gold}   ← 旧件折抵
          f2 = 价格对象 {silver, gold}   ← 新件价格
          f3 = U32  工时（分钟，观测 2/24/36）
```

客户端"选择替代品（有 N 种可选的变体）"的 N 就是 f5 的条目数；
"更改"按钮的可用性取决于该零件是否有匹配自身 systemID 的工单。

## 21/3 订购/更换零件（请求）

```
args(524) n=2:
  [0] STR  workshopID（如 "workshop_19"）
  [1] obj nfmkdcbnkhp(17510)：
      f0 = 物品 GUID
      f1 = args(503) 条目数组，元素 gcllbdkgkna(17509)：
           f0 = BYTE 零件品类（oinpmjophkh，如 52/7）
           f1 = STR  新件 systemID（如 "rgm_ef_spark10021"）
      f2 = OPT（是否用金币）
      f3 = U64（客户端算好的费用）
```

响应：更新后的物品树（`dgjinoebdop` 等），即安装完成后的竿/轮实例。
