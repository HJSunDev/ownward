'use strict';
(async () => {
  const status = document.getElementById('status');
  const bootstrap = location.hash.slice(1);
  history.replaceState(null, '', location.pathname);
  try {
    if (bootstrap) {
      const response = await fetch('v1/bootstrap', {
        method: 'POST', credentials: 'omit', cache: 'no-store', redirect: 'error',
        headers: {'Content-Type': 'application/json', 'X-Ownward-View': 'ownward.owner-view/v1'},
        body: JSON.stringify({token: bootstrap})
      });
      if (!response.ok) throw new Error('引导已失效，请从本机入口重新打开。');
      const result = await response.json();
      sessionStorage.setItem('ownward.owner-session', result.session);
    }
    const session = sessionStorage.getItem('ownward.owner-session');
    if (!session) throw new Error('请通过 ownward owner-window 从本机重新打开。');
    const response = await fetch('v1/query', {
      method: 'POST', credentials: 'omit', cache: 'no-store', redirect: 'error',
      headers: {'Content-Type': 'application/json', 'X-Ownward-View': 'ownward.owner-view/v1', Authorization: `Bearer ${session}`},
      body: JSON.stringify({view: 'health'})
    });
    if (!response.ok) { sessionStorage.removeItem('ownward.owner-session'); throw new Error('会话已失效，请从本机入口重新打开。'); }
    status.textContent = '物主验证已完成。';
  } catch (error) { status.textContent = error.message; }
})();
