export function splitLines(text) {
  return text.match(/[^\n]*\n|[^\n]+$/g) || [];
}

export function diffLines(aText, bText) {
  const a = splitLines(aText);
  const b = splitLines(bText);
  let pre = 0;
  while (pre < a.length && pre < b.length && a[pre] === b[pre]) pre++;
  let suf = 0;
  while (suf < a.length - pre && suf < b.length - pre && a[a.length - 1 - suf] === b[b.length - 1 - suf]) suf++;
  const am = a.slice(pre, a.length - suf);
  const bm = b.slice(pre, b.length - suf);
  const hunks = [];
  if (!am.length && !bm.length) return {a, b, hunks};

  const n = am.length, m = bm.length;
  const lcs = Array.from({length: n + 1}, () => new Uint32Array(m + 1));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      lcs[i][j] = am[i] === bm[j] ? lcs[i + 1][j + 1] + 1 : Math.max(lcs[i + 1][j], lcs[i][j + 1]);
    }
  }
  let i = 0, j = 0, open = null;
  const close = () => {
    if (open) {
      open.aTo = pre + i;
      open.bTo = pre + j;
      hunks.push(open);
      open = null;
    }
  };
  while (i < n || j < m) {
    if (i < n && j < m && am[i] === bm[j]) {
      close();
      i++; j++;
    } else {
      if (!open) open = {aFrom: pre + i, bFrom: pre + j};
      if (j < m && (i === n || lcs[i][j + 1] >= lcs[i + 1][j])) j++;
      else i++;
    }
  }
  close();
  return {a, b, hunks};
}
