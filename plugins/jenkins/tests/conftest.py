"""conftest: add Jenkins plugin directory to sys.path."""

import sys
from pathlib import Path

_plugin_dir = str(Path(__file__).resolve().parent.parent)
if _plugin_dir not in sys.path:
    sys.path.insert(0, _plugin_dir)
