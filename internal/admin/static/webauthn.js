function showError(msg) {
  document.getElementById('error').textContent = msg;
}

function bufToBase64url(buf) {
  const bytes = new Uint8Array(buf);
  let s = '';
  for (const b of bytes) s += String.fromCharCode(b);
  return btoa(s).replace(/\+/g, '-').replace(/\//g, '_').replace(/=+$/, '');
}

function base64urlToBuf(s) {
  s = s.replace(/-/g, '+').replace(/_/g, '/');
  while (s.length % 4) s += '=';
  const bin = atob(s);
  const buf = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) buf[i] = bin.charCodeAt(i);
  return buf.buffer;
}

async function doRegister() {
  const btn = document.getElementById('registerBtn');
  btn.disabled = true;
  showError('');
  try {
    const setupTokenEl = document.getElementById('setupToken');
    const setupToken = setupTokenEl ? setupTokenEl.value : '';
    if (setupTokenEl && !setupToken) { showError('Setup token required'); btn.disabled = false; return; }

    const beginHeaders = {method: 'POST', headers: {}};
    if (setupToken) { beginHeaders.headers['X-Setup-Token'] = setupToken; }

    const beginResp = await fetch('/admin/webauthn/register/begin', beginHeaders);
    if (!beginResp.ok) { showError((await beginResp.json()).error || 'Failed'); btn.disabled = false; return; }
    const beginData = await beginResp.json();
    const sessionToken = beginData.sessionToken;
    const opts = {publicKey: beginData.publicKey};

    opts.publicKey.challenge = base64urlToBuf(opts.publicKey.challenge);
    opts.publicKey.user.id = base64urlToBuf(opts.publicKey.user.id);
    if (opts.publicKey.excludeCredentials) {
      opts.publicKey.excludeCredentials = opts.publicKey.excludeCredentials.map(c => ({...c, id: base64urlToBuf(c.id)}));
    }

    const cred = await navigator.credentials.create(opts);
    const body = JSON.stringify({
      id: cred.id,
      rawId: bufToBase64url(cred.rawId),
      type: cred.type,
      response: {
        attestationObject: bufToBase64url(cred.response.attestationObject),
        clientDataJSON: bufToBase64url(cred.response.clientDataJSON),
      },
    });

    const finishResp = await fetch('/admin/webauthn/register/finish', {
      method: 'POST', headers: {'Content-Type': 'application/json', 'X-Session-Token': sessionToken}, body,
    });
    if (!finishResp.ok) { showError((await finishResp.json()).error || 'Registration failed'); btn.disabled = false; return; }

    window.location.href = '/admin/';
  } catch (e) {
    showError(e.message || 'Registration cancelled');
    btn.disabled = false;
  }
}

async function doLogin() {
  const btn = document.getElementById('loginBtn');
  btn.disabled = true;
  showError('');
  try {
    const beginResp = await fetch('/admin/webauthn/login/begin', {method: 'POST'});
    if (!beginResp.ok) { showError((await beginResp.json()).error || 'Failed'); btn.disabled = false; return; }
    const beginData = await beginResp.json();
    const sessionToken = beginData.sessionToken;
    const opts = {publicKey: beginData.publicKey};

    opts.publicKey.challenge = base64urlToBuf(opts.publicKey.challenge);
    if (opts.publicKey.allowCredentials) {
      opts.publicKey.allowCredentials = opts.publicKey.allowCredentials.map(c => ({...c, id: base64urlToBuf(c.id)}));
    }

    const assertion = await navigator.credentials.get(opts);
    const body = JSON.stringify({
      id: assertion.id,
      rawId: bufToBase64url(assertion.rawId),
      type: assertion.type,
      response: {
        authenticatorData: bufToBase64url(assertion.response.authenticatorData),
        clientDataJSON: bufToBase64url(assertion.response.clientDataJSON),
        signature: bufToBase64url(assertion.response.signature),
        userHandle: assertion.response.userHandle ? bufToBase64url(assertion.response.userHandle) : '',
      },
    });

    const finishResp = await fetch('/admin/webauthn/login/finish', {
      method: 'POST', headers: {'Content-Type': 'application/json', 'X-Session-Token': sessionToken}, body,
    });
    if (!finishResp.ok) { showError((await finishResp.json()).error || 'Login failed'); btn.disabled = false; return; }

    window.location.href = '/admin/';
  } catch (e) {
    showError(e.message || 'Authentication cancelled');
    btn.disabled = false;
  }
}
