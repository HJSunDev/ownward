"""Offline port fixture. No provider SDK, authentication, subprocess or network."""
from pathlib import Path
import hashlib
from external_intelligence import ExternalIntelligenceError, ExternalIntelligenceTimeout

IN_PROCESS = True
TransportError = ExternalIntelligenceError
TransportTimeout = ExternalIntelligenceTimeout

def identity_files():
    return (Path(__file__),)

def implementation_sha256():
    return hashlib.sha256(Path(__file__).read_bytes()).hexdigest()

def artifact_sha256(binary):
    return hashlib.sha256(Path(binary).read_bytes()).hexdigest()

def validate(binary, credential_file):
    if not Path(binary).is_file() or not Path(credential_file).is_file():
        raise TransportError('fixture artifacts missing')

def open_runtime(**kwargs):
    raise TransportError('offline fixture cannot invoke a model')
