(function () {
  "use strict";

  function $(id) { return document.getElementById(id); }

  function showSignedOut(message) {
    $("user-bar").hidden = true;
    $("signed-in").hidden = true;
    $("signed-out").hidden = false;
    $("session-message").textContent = message || "";
  }

  function showSignedIn(me) {
    $("signed-out").hidden = true;
    $("user-bar").hidden = false;
    $("signed-in").hidden = false;
    $("user-name").textContent = me.name;
    var list = $("characters");
    list.replaceChildren();
    (me.characters || []).forEach(function (c) {
      var li = document.createElement("li");
      li.textContent = c.name;
      list.appendChild(li);
    });
  }

  fetch("/api/me", { headers: { Accept: "application/json" } }).then(function (resp) {
    if (resp.status === 401) { showSignedOut(); return null; }
    if (!resp.ok) { throw new Error("HTTP " + resp.status); }
    return resp.json();
  }).then(function (me) {
    if (me) { showSignedIn(me); }
  }).catch(function (err) {
    showSignedOut("Cannot check your session: " + err.message);
  });
})();
