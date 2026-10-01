# 字体说明（子集化）

本目录字体是**子集化产物**，只包含代码实际用到的字符（加 ASCII 与常用标点做余量）：

- `ma_shan_zheng.ttf`（马善政楷书，26KB）：SendSheet 的「咕咕咕~」
- `caveat.ttf`（159KB）：设置页「columba」、接收页「waiting for a drop…」

若要改这两个手写文案、或新增手写样式的文字，需重新子集化（源字体：马善政 =
Google Fonts "Ma Shan Zheng"，Caveat = Google Fonts "Caveat"）：

```bash
py -3 -m fontTools.subset ma_shan_zheng.ttf --text="新的文案" \
  --unicodes="U+0020-007E,U+2026,U+3000-3011,U+FF01-FF5E" --output-file=ma_shan_zheng.ttf
py -3 -m fontTools.subset caveat.ttf \
  --unicodes="U+0020-007E,U+00A0-00FF,U+2013-2026" --output-file=caveat.ttf
```

不要直接放全量字体：中文全量字体约 6MB，与项目「小体积」优先级冲突。
