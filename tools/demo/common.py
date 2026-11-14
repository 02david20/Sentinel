"""Drive a real program in a pseudo-terminal, emulate the screen with pyte, and write PNG frames.

Used by record.py (the TUI) and record_plugin.py (a shell session about plugin blocks)."""
import fcntl, hashlib, os, pty, select, shutil, struct, termios, time
import pyte
from PIL import Image, ImageDraw, ImageFont

FS, CH, PAD = 18, 23, 18
BG, FG = (30, 30, 30), (216, 216, 216)
_menlo = '/System/Library/Fonts/Menlo.ttc'
reg = ImageFont.truetype(_menlo, FS, index=0)
bld = ImageFont.truetype(_menlo, FS, index=1)
_fallbacks = [ImageFont.truetype('/System/Library/Fonts/Supplemental/Arial Unicode.ttf', FS),
              ImageFont.truetype('/System/Library/Fonts/Apple Symbols.ttf', FS)]
CW = round(reg.getlength('M'))
NAMED = {'black': (0, 0, 0), 'red': (205, 49, 49), 'green': (13, 188, 121), 'brown': (229, 229, 16), 'yellow': (229, 229, 16),
         'blue': (36, 114, 200), 'magenta': (188, 63, 188), 'cyan': (17, 168, 205), 'white': (229, 229, 229),
         'brightblack': (102, 102, 102), 'brightred': (241, 76, 76), 'brightgreen': (35, 209, 139),
         'brightbrown': (245, 245, 67), 'brightyellow': (245, 245, 67), 'brightblue': (59, 142, 234),
         'brightmagenta': (214, 112, 214), 'brightcyan': (41, 184, 219), 'brightwhite': (255, 255, 255)}


def _col(c, default):
    if c == 'default':
        return default
    if c in NAMED:
        return NAMED[c]
    if len(c) == 6:
        try:
            return tuple(int(c[i:i + 2], 16) for i in (0, 2, 4))
        except ValueError:
            pass
    return default


def _missing(f, c):
    return f.getmask(c).getbbox() == f.getmask('￿').getbbox()


def render(screen, cols, rows, size):
    im = Image.new('RGB', size, BG)
    d = ImageDraw.Draw(im)
    for y in range(rows):
        line = screen.buffer[y]
        for x in range(cols):
            ch = line[x]
            fg, bg = _col(ch.fg, FG), _col(ch.bg, BG)
            if ch.reverse:
                fg, bg = bg, fg
            px, py = PAD + x * CW, PAD + y * CH
            if bg != BG:
                d.rectangle([px, py, px + CW - 1, py + CH - 1], fill=bg)
            if ch.data.strip():
                f = bld if ch.bold else reg
                for alt in _fallbacks:
                    if _missing(f, ch.data):
                        f = alt
                d.text((px, py + 1), ch.data, font=f, fill=fg)
    return im


def session(argv, env, cwd, script, out_dir, cols=100, rows=34, fps=12, tail=2.5):
    """script: list of (seconds_to_wait_before, bytes_to_send). Returns the number of frames."""
    shutil.rmtree(out_dir, ignore_errors=True)
    os.makedirs(out_dir)
    w, h = cols * CW + 2 * PAD, rows * CH + 2 * PAD
    size = (w + w % 2, h + h % 2)
    pid, fd = pty.fork()
    if pid == 0:
        os.chdir(cwd)
        os.execvpe(argv[0], argv, env)
    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack('HHHH', rows, cols, 0, 0))
    screen = pyte.Screen(cols, rows)
    stream = pyte.ByteStream(screen)

    def handle(data):  # answer the terminal queries that Bubble Tea / termenv send at start-up
        try:
            if b'\x1b[6n' in data: os.write(fd, b'\x1b[1;1R')
            if b'\x1b]11;?' in data: os.write(fd, b'\x1b]11;rgb:1e1e/1e1e/1e1e\x1b\\')
            if b'\x1b]10;?' in data: os.write(fd, b'\x1b]10;rgb:d8d8/d8d8/d8d8\x1b\\')
            if b'\x1b[c' in data or b'\x1b[0c' in data: os.write(fd, b'\x1b[?62;c')
        except OSError:
            pass
        stream.feed(data)

    schedule, t = [], 0.0
    for wait, k in script:
        t += wait
        schedule.append((t, k))
    end = t + tail
    t0, n, idx, last_hash, last_img = time.time(), 0, 0, None, None
    while True:
        now = time.time() - t0
        while idx < len(schedule) and now >= schedule[idx][0]:
            try: os.write(fd, schedule[idx][1])
            except OSError: pass
            idx += 1
        r, _, _ = select.select([fd], [], [], 0.005)
        if r:
            try: data = os.read(fd, 65536)
            except OSError: data = b''
            if not data and idx >= len(schedule): break
            if data: handle(data)
        if now >= n / fps:
            while select.select([fd], [], [], 0.025)[0]:  # let the program finish repainting
                try: data = os.read(fd, 65536)
                except OSError: break
                if not data: break
                handle(data)
            h_ = hashlib.md5(repr([[(c.data, c.fg, c.bg, c.bold, c.reverse) for c in (screen.buffer[y][x] for x in range(cols))] for y in range(rows)]).encode()).hexdigest()
            if h_ != last_hash:
                last_img, last_hash = render(screen, cols, rows, size), h_
            last_img.save(f'{out_dir}/f{n:05d}.png')
            n += 1
        if now > end and idx >= len(schedule):
            break
    try: os.kill(pid, 9)
    except OSError: pass
    return n, size
