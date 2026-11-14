"""Records the plugin-block demo (docs/demo-plugins.mp4): a real shell session. See run.sh."""
import os, shutil, sys
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import common

S = os.path.dirname(os.path.abspath(__file__))
W = '/tmp/sentinel-demo'  # short paths read better in the video
shutil.rmtree(W, ignore_errors=True)
os.makedirs(os.path.join(W, 'workflows'))
os.makedirs(os.path.join(W, 'bin'))
shutil.copy(os.path.join(S, 'sentinel'), os.path.join(W, 'bin', 'sentinel'))
open(os.path.join(W, 'workflows', 'shout.yaml'), 'w').write('''name: shout
summary: "shouted ${s.result}"
steps:
  - id: s
    block: Text.Shout
    in: { text: hello }
''')

CLEAR = b'\x0c'
ENTER = b'\r'


def typed(text, pause=0.6):
    """Type a command character by character, then Enter."""
    out = [(pause, text[0].encode())]
    out += [(0.035, c.encode()) for c in text[1:]]
    out.append((0.4, ENTER))
    return out


def say(text, pause=1.0):  # a comment line: bash ignores it, the video reads it
    return typed('# ' + text, pause)


SCRIPT = [(1.0, CLEAR)]
SCRIPT += say('An agent needs a step that does not exist yet. It scaffolds one:', 0.5)
SCRIPT += typed('sentinel block new Text.Shout', 0.8) + [(2.0, b'')]
SCRIPT += typed('sentinel block check Text.Shout', 0.8) + [(2.4, b'')]
SCRIPT += [(0.5, CLEAR)]
SCRIPT += say('A workflow cannot use it yet: nobody has approved it.', 0.4)
SCRIPT += typed('sentinel validate workflows/shout.yaml; echo "exit code $?"', 0.8) + [(2.4, b'')]
SCRIPT += [(0.8, CLEAR)]
SCRIPT += say('Only a person can approve. The review comes BEFORE any code runs:', 0.4)
SCRIPT += typed('sentinel block approve Text.Shout', 0.8) + [(3.8, b'y'), (0.6, ENTER), (3.5, b'')]
SCRIPT += [(0.8, CLEAR)]
SCRIPT += typed('sentinel run workflows/shout.yaml', 0.6) + [(2.4, b'')]
SCRIPT += say('Now the block is edited after approval...', 0.9)
SCRIPT += typed("echo '# sneaky change' >> $SENTINEL_BLOCKS_DIR/Text.Shout/block.py", 0.6) + [(1.2, b'')]
SCRIPT += typed('sentinel run workflows/shout.yaml; echo "exit code $?"', 0.8) + [(3.5, b'')]
SCRIPT += [(0.8, CLEAR)]
SCRIPT += say('Re-approving shows exactly what changed:', 0.4)
SCRIPT += typed('sentinel block approve Text.Shout', 0.8) + [(4.2, b'y'), (0.6, ENTER), (3.5, b'')]
SCRIPT += [(0.8, CLEAR)]
SCRIPT += say('The workflow and its external block, in the TUI:', 0.4)
SCRIPT += typed('sentinel', 0.8) + [(2.5, b'\r'), (4.5, b'q'), (1.5, b'')]

# TERM=screen-*: Bubble Tea/termenv then skip their start-up colour query, which a scripted
# terminal would otherwise echo into the shell
env = dict(os.environ, TERM='screen-256color', COLORTERM='truecolor', LANG='en_US.UTF-8',
           SENTINEL_STATE_DIR=os.path.join(W, 'state'), SENTINEL_BLOCKS_DIR=os.path.join(W, 'state', 'blocks.d'), PATH=os.path.join(W, 'bin') + ':' + os.environ['PATH'],
           PS1='\\[\\e[1;32m\\]$\\[\\e[0m\\] ', HISTFILE='/dev/null')
# a first run so the TUI has something to show: done inside the session, after approval
n, size = common.session(['bash', '--noprofile', '--norc', '-i'], env, W, SCRIPT, os.path.join(S, 'frames-plugin'), rows=34)
print('frames', n, 'size', size)
