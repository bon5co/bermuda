# /// script
# dependencies = ["pillow"]
# ///
import json, sys
from PIL import Image, ImageDraw, ImageFont

FONT = "/nix/store/pbdvxcdrvrsmg3sn0h26l5pgwm51l8ly-dejavu-fonts-2.37/share/fonts/truetype/DejaVuSansMono.ttf"
data = json.load(sys.stdin)
size = 22
font = ImageFont.truetype(FONT, size)
small = ImageFont.truetype(FONT, 16)
cw = font.getlength("M")
ch = size * 1.25
colors = {"dim": "#7c8190", "claude": "#d77757", "success": "#6cc08a", "warning": "#e5b567", "error": "#e06c75"}
bg, fg = "#1a1b26", "#c0c5d6"
pad = 24
maxcols = max(p["columns"] for p in data["panels"])
width = int(maxcols * cw + pad * 2)
blocks = [(f'{p["columns"]} columns', p["rows"]) for p in data["panels"]]
height = int(sum(ch * 2 + 48 for _ in blocks) + pad)
img = Image.new("RGB", (width, height), bg)
d = ImageDraw.Draw(img)
y = pad
for p in data["panels"]:
    d.text((pad, y), f'{p["columns"]} columns', font=small, fill="#565b70")
    y += 26
    d.rectangle([pad - 6, y - 4, pad + p["columns"] * cw + 6, y + ch * 2 + 4], outline="#2f3347")
    for row in p["rows"]:
        x = pad
        for cell in row:
            d.text((x, y), cell["t"], font=font, fill=colors.get(cell["c"], fg))
            x += cw
        y += ch
    y += 22
img.save(sys.argv[1])
