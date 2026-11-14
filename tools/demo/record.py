"""Records the TUI demo (docs/demo.mp4). See run.sh."""
import os, sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import common

S = os.path.dirname(os.path.abspath(__file__))
UP, DOWN, ENTER, ESC = b'\x1b[A', b'\x1b[B', b'\r', b'\x1b'
SCRIPT = [
    (2.0, DOWN), (1.2, UP), (1.2, ENTER),       # open price-watch
    (2.6, DOWN), (2.2, DOWN), (2.8, b'l'),       # dry run, then the failed run, then its log
    (3.0, b'l'), (1.2, UP), (1.5, UP), (1.5, UP), (1.5, ESC),
    (1.2, b'd'), (4.0, b'?'), (3.0, b' '), (1.0, b'q'),
]
env = dict(os.environ, TERM='xterm-256color', COLORTERM='truecolor', LANG='en_US.UTF-8', SENTINEL_STATE_DIR=os.path.join(S, 'state'))
n, size = common.session([os.path.join(S, 'sentinel')], env, os.path.join(S, 'demo'), SCRIPT, os.path.join(S, 'frames'), rows=40)
print('frames', n, 'size', size)
