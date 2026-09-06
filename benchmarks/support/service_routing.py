"""Optional, task-local failover; independent of models and service providers."""
from __future__ import annotations

import threading

from external_intelligence import ExternalIntelligenceError


class ServiceError(ExternalIntelligenceError):
    def __init__(self, message: str, code: str = "") -> None:
        super().__init__(message)
        self.code = code


class ServiceRoute:
    def __init__(self, primary: str, fallback: str | None = None,
                 error_codes: tuple[str, ...] = ()) -> None:
        self.primary = primary
        self.fallback = fallback
        self.error_codes = error_codes
        self._current = primary
        self._lock = threading.Lock()

    def new_scope(self) -> ServiceRoute:
        return ServiceRoute(self.primary, self.fallback, self.error_codes)

    @property
    def current(self) -> str:
        with self._lock:
            return self._current

    def after_error(self, service: str, error: ServiceError) -> str | None:
        with self._lock:
            if service != self.primary or not self.fallback or error.code not in self.error_codes:
                return None
            # Concurrent requests already sent to primary may fail independently.
            self._current = self.fallback
            return self._current
