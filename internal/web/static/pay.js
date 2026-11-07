// pay.js runs on the page of an order that can be paid. It reloads the page every 5 seconds, as
// the meta refresh in the page's noscript does when JavaScript is off.

const RELOAD_MS = 5000;

setTimeout(() => location.reload(), RELOAD_MS);
