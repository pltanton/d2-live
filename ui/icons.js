const paths = {
  pencil: 'M10.8 2.7l2.5 2.5L6 12.5l-3.2.7.7-3.2z M9.3 4.2l2.5 2.5',
  arrow: 'M2.5 8h10 M9 4.5L12.5 8 9 11.5',
  swap: 'M2.5 5.5h10 M10 3l2.5 2.5L10 8 M13.5 10.5h-10 M6 8l-2.5 2.5L6 13',
  trash: 'M2.5 4.5h11 M6 4.5V2.8h4v1.7 M4 4.5l.8 9h6.4l.8-9 M6.6 7v4.2 M9.4 7v4.2',
  undo: 'M5.5 3.5L2.5 6.5l3 3 M2.5 6.5h7a3.5 3.5 0 0 1 0 7H7',
  redo: 'M10.5 3.5l3 3-3 3 M13.5 6.5h-7a3.5 3.5 0 0 0 0 7H9',
  fit: 'M2.5 6V2.5H6 M10 2.5h3.5V6 M13.5 10v3.5H10 M6 13.5H2.5V10',
  plus: 'M8 3v10 M3 8h10',
  up: 'M8 13V3.5 M4.5 7L8 3.5 11.5 7',
  down: 'M8 3v9.5 M4.5 9L8 12.5 11.5 9',
};

export function icon(name) {
  const svg = document.createElementNS('http://www.w3.org/2000/svg', 'svg');
  svg.setAttribute('viewBox', '0 0 16 16');
  svg.setAttribute('class', 'd2l-ic');
  svg.setAttribute('aria-hidden', 'true');
  const path = document.createElementNS('http://www.w3.org/2000/svg', 'path');
  path.setAttribute('d', paths[name]);
  svg.appendChild(path);
  return svg;
}
