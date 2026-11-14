import os, pty, sys, time, select, struct, fcntl, termios, shutil, hashlib
import pyte
from PIL import Image, ImageDraw, ImageFont

S = os.path.dirname(os.path.abspath(__file__))
COLS, ROWS, FPS = 100, 40, 12
OUT = os.path.join(S, 'frames'); shutil.rmtree(OUT, ignore_errors=True); os.makedirs(OUT)

FS = 18
reg = ImageFont.truetype('/System/Library/Fonts/Menlo.ttc', FS, index=0)
bld = ImageFont.truetype('/System/Library/Fonts/Menlo.ttc', FS, index=1)
sym = ImageFont.truetype('/System/Library/Fonts/Apple Symbols.ttf', FS)
uni = ImageFont.truetype('/System/Library/Fonts/Supplemental/Arial Unicode.ttf', FS)
miss = lambda f, c: f.getmask(c).getbbox() == f.getmask('\uffff').getbbox()
CW = round(reg.getlength('M')); CH = 23; PAD = 18
BG, FG = (30, 30, 30), (216, 216, 216)
NAMED = {'black': (0,0,0), 'red': (205,49,49), 'green': (13,188,121), 'brown': (229,229,16), 'yellow': (229,229,16),
 'blue': (36,114,200), 'magenta': (188,63,188), 'cyan': (17,168,205), 'white': (229,229,229),
 'brightblack': (102,102,102), 'brightred': (241,76,76), 'brightgreen': (35,209,139), 'brightbrown': (245,245,67),
 'brightyellow': (245,245,67), 'brightblue': (59,142,234), 'brightmagenta': (214,112,214), 'brightcyan': (41,184,219),
 'brightwhite': (255,255,255)}
def col(c, default):
    if c == 'default': return default
    if c in NAMED: return NAMED[c]
    if len(c) == 6:
        try: return tuple(int(c[i:i+2], 16) for i in (0, 2, 4))
        except ValueError: pass
    return default

W, H = COLS*CW + 2*PAD, ROWS*CH + 2*PAD
W += W % 2; H += H % 2

def render(screen):
    im = Image.new('RGB', (W, H), BG); d = ImageDraw.Draw(im)
    for y in range(ROWS):
        line = screen.buffer[y]
        for x in range(COLS):
            ch = line[x]
            fg, bg = col(ch.fg, FG), col(ch.bg, BG)
            if ch.reverse: fg, bg = bg, fg
            px, py = PAD + x*CW, PAD + y*CH
            if bg != BG: d.rectangle([px, py, px+CW-1, py+CH-1], fill=bg)
            if ch.data.strip():
                f = bld if ch.bold else reg
                for alt in (uni, sym):
                    if miss(f, ch.data): f = alt
                d.text((px, py+1), ch.data, font=f, fill=fg)
    return im

# keystroke script: (seconds to wait before, bytes)
UP, DOWN, ENTER, ESC = b'\x1b[A', b'\x1b[B', b'\r', b'\x1b'
SCRIPT = [
  (2.0, DOWN), (1.2, UP), (1.2, ENTER),      # open price-watch
  (2.6, DOWN), (2.2, DOWN), (2.8, b'l'),      # dry run, then the failed run, then its log
  (3.0, b'l'), (1.2, UP), (1.5, UP), (1.5, UP), (1.5, ESC),
  (1.2, b'd'), (4.0, b'?'), (3.0, b' '), (1.0, b'q'),
]

env = dict(os.environ, TERM='xterm-256color', COLORTERM='truecolor', SENTINEL_STATE_DIR=os.path.join(S, 'state'), LANG='en_US.UTF-8')
pid, fd = pty.fork()
if pid == 0:
    os.chdir(os.path.join(S, 'demo'))
    os.execve(os.path.join(S, 'sentinel'), ['sentinel'], env)
fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', ROWS, COLS, 0, 0))
screen = pyte.Screen(COLS, ROWS); stream = pyte.ByteStream(screen)

FIRST = []
def handle(data):
    if b'\x1b[6n' in data: os.write(fd, b'\x1b[1;1R')
    if b'\x1b]11;?' in data: os.write(fd, b'\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\')
    if b'\x1b]10;?' in data: os.write(fd, b'\x1b]10;rgb:d8d8/d8d8/d8d8\x1b\\')
    if b'\x1b[c' in data or b'\x1b[0c' in data: os.write(fd, b'\x1b[?62;c')
    stream.feed(data)
t0 = time.time(); n = 0; nextkey = 0; idx = 0
schedule = []; t = 0
for wait, k in SCRIPT: t += wait; schedule.append((t, k))
end = t + 1.5; last_hash = None; last_img = None
while True:
    now = time.time() - t0
    while idx < len(schedule) and now >= schedule[idx][0]:
        os.write(fd, schedule[idx][1]); idx += 1
    r, _, _ = select.select([fd], [], [], 0.005)
    if r:
        try:
            data = os.read(fd, 65536)
        except OSError:
            data = b''
        if not data and idx >= len(schedule): break
        if data: handle(data)
    if now >= n / FPS:
        # wait for the program to finish repainting so no frame shows a half-drawn screen
        while select.select([fd], [], [], 0.025)[0]:
            try: data = os.read(fd, 65536)
            except OSError: break
            if not data: break
            handle(data)
        h = hashlib.md5(repr([[(c.data, c.fg, c.bg, c.bold, c.reverse) for c in (screen.buffer[y][x] for x in range(COLS))] for y in range(ROWS)]).encode()).hexdigest()
        if h != last_hash: last_img, last_hash = render(screen), h
        last_img.save(f'{OUT}/f{n:05d}.png'); n += 1
    if now > end + 3 and idx >= len(schedule): break
print('frames', n, 'size', W, H)

