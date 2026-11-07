// pay.js runs on the page of an order that can be paid, and pays it with a browser wallet. A
// wallet extension on a computer, such as Phantom, Solflare or Backpack, opens no solana: link,
// but announces itself to the page through the Wallet Standard. For each wallet that can sign and
// send a devnet transaction, the script shows a button. A click connects the wallet, asks the shop
// for the transaction that pays the order from the wallet's account, and has the wallet sign and
// send it. Without a wallet the script shows nothing.
//
// It also reloads the page every 5 seconds, as the meta refresh in the page's noscript does when
// JavaScript is off, but not while a payment is in progress: the reload would drop the wallet's
// request for approval.

const CHAIN = 'solana:devnet';
const RELOAD_MS = 5000;
// A transaction's blockhash is at most 10 seconds old when the shop builds it, and the network
// takes it for about 60 to 90 seconds: 2 minutes later, a transaction that has not landed never
// will. That is how long the buttons stay off after a send, and how long a wallet has to answer.
const EXPIRY_MS = 2 * 60 * 1000;
const BASE58 = '123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz';

let reload = setTimeout(() => location.reload(), RELOAD_MS); // held while a payment is in progress
let busy = false; // a payment is in progress
let held = false; // a payment sent from this tab may still land, so the buttons stay off
const box = document.getElementById('wallets'); // hidden until it has a button
const line = document.getElementById('pay-status');
const order = box.dataset.order;
const sentKey = `solpay-shop:sent:${order}`;
const names = new Set(); // of the wallets that have a button

// Problem is an error whose message is written for the customer.
class Problem extends Error {}

// A payment sent from this tab before a reload is still waiting for the shop to see it: once the
// shop has, the page shows the order paid and loads no script.
try {
  const sent = JSON.parse(sessionStorage.getItem(sentKey));
  if (sent) saySent(sent);
} catch {
  // Storage is blocked, so the note does not outlive the reload, or it holds no note.
}

// The Wallet Standard's handshake. The page announces itself with app-ready, which a wallet that
// loaded first answers by calling register. A wallet that loads later dispatches register-wallet,
// whose detail is a function that the page calls with the same register.
const api = Object.freeze({
  register(...wallets) {
    for (const wallet of wallets) add(wallet);
    return () => {}; // a wallet that unregisters keeps its button until the next reload
  },
});
window.addEventListener('wallet-standard:register-wallet', (event) => event.detail(api));
window.dispatchEvent(new CustomEvent('wallet-standard:app-ready', { detail: api }));

// add shows a button for wallet if it can connect and sign and send on devnet, and no wallet of
// the same name has one.
function add(wallet) {
  const features = wallet?.features ?? {};
  const usable = wallet?.chains?.includes(CHAIN) && features['standard:connect'] &&
    features['solana:signAndSendTransaction'];
  if (!usable || names.has(wallet.name)) return;
  names.add(wallet.name);
  const button = document.createElement('button');
  button.type = 'button';
  button.disabled = busy || held;
  if (typeof wallet.icon === 'string' && wallet.icon.startsWith('data:image/')) {
    const icon = new Image(20, 20);
    icon.src = wallet.icon;
    icon.alt = '';
    button.append(icon);
  }
  button.append(`Pay with ${wallet.name}`);
  button.addEventListener('click', () => pay(wallet, button));
  box.append(button);
  box.hidden = false;
}

// pay pays the order with wallet, whose button is button, saying on the status line how it goes.
async function pay(wallet, button) {
  if (busy || held) return;
  setBusy(true);
  try {
    say(`Connecting to ${wallet.name}. Approve in the wallet if it asks.`);
    const connected = await answer(wallet, wallet.features['standard:connect'].connect());
    const accounts = connected?.accounts ?? [];
    const account = accounts.find((a) => a.chains?.includes(CHAIN)) ?? accounts[0];
    if (!account) throw new Problem(`${wallet.name} shared no account.`);
    say('Preparing the payment.');
    const transaction = await fetchTransaction(account.address);
    say(`Approve the payment in ${wallet.name}.`);
    const outputs = await answer(wallet, wallet.features['solana:signAndSendTransaction']
      .signAndSendTransaction({ account, transaction, chain: CHAIN }));
    const signature = outputs?.[0]?.signature;
    if (!(signature instanceof Uint8Array) || signature.length !== 64) {
      throw new Problem(`${wallet.name} did not return the payment's signature. ` +
        "Check the wallet's activity before paying again.");
    }
    const sent = { sig: base58(signature), at: Date.now() };
    try {
      sessionStorage.setItem(sentKey, JSON.stringify(sent));
    } catch {
      // Storage is blocked; the note does not outlive the reload.
    }
    saySent(sent);
  } catch (err) {
    say(describe(err, wallet.name), true);
  } finally {
    setBusy(false); // after a send, the buttons stay off
    // Disabling the clicked button moved the focus to the body, so it gets the focus back.
    if (document.activeElement === document.body && !button.disabled) button.focus();
  }
}

// answer waits at most EXPIRY_MS for promise, which a call to wallet returned. A wallet that never
// answers, as one may once its window is closed without a choice, would otherwise hold the reload
// for good; and a transaction that it approves later has expired by then.
function answer(wallet, promise) {
  let timer;
  const late = new Promise((_, reject) => {
    timer = setTimeout(reject, EXPIRY_MS, new Problem(`No answer from ${wallet.name}. Try again.`));
  });
  return Promise.race([promise, late]).finally(() => clearTimeout(timer));
}

// fetchTransaction asks the shop for the unsigned transaction with which address pays the order,
// and returns its bytes.
async function fetchTransaction(address) {
  const failed = new Problem('The shop could not prepare the payment. Try again.');
  const res = await fetch(`/api/v1/orders/${encodeURIComponent(order)}/transaction`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ account: address }),
  }).catch(() => {
    throw failed;
  });
  if (res.status === 409) throw new Problem('The order can no longer be paid, so the page reloads.');
  if (!res.ok) throw failed;
  try {
    const { transaction } = await res.json();
    return Uint8Array.from(atob(transaction), (c) => c.charCodeAt(0));
  } catch {
    throw failed;
  }
}

// describe says what went wrong in plain words, never with a stack trace. 4001 is the code that
// EIP-1193 gives a request the user rejected, which Phantom's documentation lists too; a wallet
// that says so another way has its own message shown.
function describe(err, name) {
  if (err instanceof Problem) return err.message;
  if (err?.code === 4001) return `You declined in ${name}; nothing was paid.`;
  const message = typeof err?.message === 'string' ? err.message.trim() : '';
  return message ? `${name} reported an error: ${message}` : `${name} reported an error. Try again.`;
}

// setBusy disables the buttons while a payment is in progress, and holds the page's reload. Once
// the payment is over, the reload is 5 seconds away again, so the status line stays that long,
// and the buttons are back unless a payment sent from this tab may still land.
function setBusy(on) {
  busy = on;
  for (const button of box.querySelectorAll('button')) button.disabled = busy || held;
  clearTimeout(reload);
  if (!on) reload = setTimeout(() => location.reload(), RELOAD_MS);
}

// say puts text on the status line, marked as a problem if bad.
function say(text, bad = false) {
  line.textContent = text;
  line.classList.toggle('problem', bad);
}

// saySent says that the payment with signature sig was sent at at, in ms since the epoch, and links
// to it on the devnet explorer. Until the payment can no longer land, the buttons stay off, so
// that a customer who waits for the shop to see it does not pay twice.
function saySent({ sig, at }) {
  const link = document.createElement('a');
  link.href = `https://explorer.solana.com/tx/${encodeURIComponent(sig)}?cluster=devnet`;
  link.target = '_blank';
  link.rel = 'noopener';
  link.textContent = sig;
  held = Date.now() - at < EXPIRY_MS;
  line.classList.toggle('problem', !held);
  line.replaceChildren(...(held
    ? ['Payment sent: ', link, '. The shop is waiting to see it on devnet; this page updates by ' +
      'itself. The Pay buttons stay off for 2 minutes after a payment, so that the order is not ' +
      'paid twice.']
    : ['The shop has not seen payment ', link, ' yet. A payment that has not landed 2 minutes ' +
      'after it was sent never will, so if the explorer does not find it, you can pay again.']));
}

// base58 writes bytes as Solana writes signatures and addresses.
function base58(bytes) {
  const digits = []; // in base 58, the least significant first
  for (const byte of bytes) {
    let carry = byte;
    for (let i = 0; i < digits.length; i++) {
      carry += digits[i] * 256;
      digits[i] = carry % 58;
      carry = Math.floor(carry / 58);
    }
    for (; carry > 0; carry = Math.floor(carry / 58)) digits.push(carry % 58);
  }
  let out = '';
  for (let i = 0; i < bytes.length && bytes[i] === 0; i++) out += '1'; // each leading zero byte
  for (let i = digits.length - 1; i >= 0; i--) out += BASE58[digits[i]];
  return out;
}
