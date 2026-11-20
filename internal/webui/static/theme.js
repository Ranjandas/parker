// Applies the saved theme before the page paints.
try {
  const t = localStorage.getItem("parker-theme");
  if (t === "light" || t === "dark") document.documentElement.dataset.theme = t;
} catch (e) { /* storage blocked: follow the system */ }
