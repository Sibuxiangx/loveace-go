"use strict";

// The verification page: a user types the code shown on a device, signs in
// with school credentials, and (when the school asks) solves a CAPTCHA. The
// device receives its tokens by polling; this page only approves.
(() => {
  const $ = (id) => document.getElementById(id);
  const steps = {
    code: $("card-code"),
    login: $("card-login"),
    captcha: $("card-captcha"),
    done: $("card-done"),
  };
  const subtitles = {
    code: "输入设备屏幕上显示的设备码",
    login: "使用 AUFE 统一认证账号",
    captcha: "AUFE 网关要求先通过验证码",
    done: "授权已完成",
  };
  const state = { userCode: "", challengeId: "" };

  function renderIcons() {
    for (const el of document.querySelectorAll("[data-icon]")) {
      const path = window.ICONS && window.ICONS[el.dataset.icon];
      if (!path) continue;
      el.innerHTML =
        `<svg viewBox="0 0 960 960" aria-hidden="true" focusable="false">` +
        `<path fill="currentColor" d="${path}"/></svg>`;
    }
  }

  function setBanner(text) {
    $("banner").hidden = !text;
    $("banner-text").textContent = text;
  }

  function showStep(name) {
    for (const [key, el] of Object.entries(steps)) el.hidden = key !== name;
    $("subtitle").textContent = subtitles[name];
    setBanner("");
  }

  async function call(method, url, body) {
    const init = {
      method,
      cache: "no-store",
      credentials: "omit",
      referrerPolicy: "no-referrer",
      headers: {},
    };
    if (body !== undefined) {
      init.headers["Content-Type"] = "application/json";
      init.body = JSON.stringify(body);
    }
    const res = await fetch(url, init);
    let data = null;
    try { data = await res.json(); } catch (_) { data = null; }
    return { ok: res.ok, status: res.status, data };
  }

  const detail = (r) => (r.data && r.data.detail) || "请求失败，请稍后重试";
  const captchaUrl = (id) =>
    `/v1/auth/challenges/${encodeURIComponent(id)}/image?ts=${Date.now()}`;

  // run disables the form and shows `label` on its primary button while fn runs.
  async function run(form, label, fn) {
    const primary = form.querySelector(".btn.filled .btn-label");
    const idle = primary.textContent;
    for (const el of form.querySelectorAll("input, button")) el.disabled = true;
    primary.textContent = label;
    try {
      await fn();
    } catch (_) {
      setBanner("网络异常，请稍后重试");
    } finally {
      for (const el of form.querySelectorAll("input, button")) el.disabled = false;
      primary.textContent = idle;
    }
  }

  async function lookup(code) {
    const r = await call("GET", "/v1/auth/device/info?user_code=" + encodeURIComponent(code));
    if (!r.ok) {
      setBanner(detail(r));
      return;
    }
    state.userCode = code;
    const platform = r.data.platform ? `（${r.data.platform}）` : "";
    $("device-info").textContent =
      `设备「${r.data.client_name}」${platform}请求访问你的账号`;
    showStep("login");
  }

  $("card-code").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const code = $("user-code").value.trim();
    if (!code) {
      setBanner("请输入设备码");
      return;
    }
    run($("card-code"), "查询中", () => lookup(code));
  });

  $("toggle-gw").addEventListener("click", () => {
    const field = $("gw-field");
    field.hidden = !field.hidden;
    $("toggle-gw").setAttribute("aria-expanded", String(!field.hidden));
  });

  $("card-login").addEventListener("submit", (ev) => {
    ev.preventDefault();
    if (!$("agree").checked) {
      setBanner("请先阅读并同意用户服务协议和隐私政策");
      return;
    }
    const account = $("account").value.trim();
    const password = $("password").value;
    const gateway = $("gateway").value;
    $("password").value = "";
    $("gateway").value = "";
    if (!account || !password) {
      setBanner("请输入学号和学校密码");
      return;
    }
    run($("card-login"), "验证中", async () => {
      const body = { user_code: state.userCode, account, password };
      if (gateway) body.gateway_password = gateway;
      const r = await call("POST", "/v1/auth/device/verify", body);
      if (r.ok) {
        showStep("done");
        return;
      }
      if (r.status === 409 && r.data && r.data.challenge_id) {
        state.challengeId = r.data.challenge_id;
        $("captcha-img").src = captchaUrl(state.challengeId);
        showStep("captcha");
        return;
      }
      setBanner(detail(r));
    });
  });

  function refreshCaptcha() {
    $("captcha-img").src = captchaUrl(state.challengeId);
  }
  $("captcha-btn").addEventListener("click", refreshCaptcha);
  $("captcha-refresh").addEventListener("click", refreshCaptcha);
  $("captcha-back").addEventListener("click", () => showStep("login"));

  $("card-captcha").addEventListener("submit", (ev) => {
    ev.preventDefault();
    const code = $("captcha-code").value.trim();
    $("captcha-code").value = "";
    run($("card-captcha"), "验证中", async () => {
      const path = `/v1/auth/device/challenges/${encodeURIComponent(state.challengeId)}/solution`;
      const r = await call("POST", path, { user_code: state.userCode, code });
      if (r.ok) {
        showStep("done");
        return;
      }
      if (r.status === 404 || r.status === 409) {
        showStep("login");
        setBanner(detail(r));
        return;
      }
      setBanner(detail(r));
      refreshCaptcha();
    });
  });

  renderIcons();
  showStep("code");
  const prefill = new URLSearchParams(location.search).get("user_code");
  if (prefill) {
    $("user-code").value = prefill;
    run($("card-code"), "查询中", () => lookup(prefill.trim()));
  }
})();
