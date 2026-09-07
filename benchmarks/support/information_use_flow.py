"""模拟接入方复用产品的同一信息使用流程，不维护评测专用实现。"""
import importlib.util
from pathlib import Path

_PATH = Path(__file__).resolve().parents[2] / 'integrations/python/ownward_information_use.py'
_SPEC = importlib.util.spec_from_file_location('ownward_information_use', _PATH)
_MODULE = importlib.util.module_from_spec(_SPEC)
_SPEC.loader.exec_module(_MODULE)

complete = _MODULE.complete
reconsider = _MODULE.reconsider
respond = _MODULE.respond
finish = _MODULE.finish
OFFER = _MODULE.OFFER
RESPONSE = _MODULE.RESPONSE
