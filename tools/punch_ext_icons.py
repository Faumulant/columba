"""把 drop-icon-bw 的圆角外白色改成透明，供浏览器扩展使用。

为什么需要这一步：那套图标原图是「白底 + 黑圆角块 + 白鸟」，白底不透明。
浏览器工具栏在深色主题下是深灰底，直接贴会露出一个白色方块（实测预览可见）。
这里只把圆角矩形**外面**的像素置透明，块内保持原样。
"""
import os
from PIL import Image, ImageDraw

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SIZES = [16, 32, 48, 128]


def punch_rounded(im, radius_ratio=0.22):
    """按圆角矩形做遮罩：块内保留、块外透明。"""
    size = im.size[0]
    ss = 4
    mask = Image.new("L", (size * ss, size * ss), 0)
    d = ImageDraw.Draw(mask)
    r = int(size * ss * radius_ratio)
    d.rounded_rectangle([0, 0, size * ss - 1, size * ss - 1], radius=r, fill=255)
    mask = mask.resize((size, size), Image.LANCZOS)

    out = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    src = im.convert("RGBA")
    out.paste(src, (0, 0), mask)
    return out


def main():
    for browser in ("chromium", "firefox"):
        for s in SIZES:
            p = os.path.join(ROOT, "extension", browser, "icons", "icon%d.png" % s)
            im = Image.open(p).convert("RGBA")
            punch_rounded(im).save(p, optimize=True)
        print("已处理", browser)


if __name__ == "__main__":
    main()
