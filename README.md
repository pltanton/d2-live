# d2-live

Small local D2 preview server for `*.d2` files.

What it does:
- live SVG preview with pan and zoom
- PNG render through native `d2` export
- sketch mode
- copy / download SVG and PNG
- zoomable UI with preserving position, for seamless re-rendering

## Run

```bash
d2-live diagram.d2
```

## Flags

- `--layout elk|dagre|...` choose the D2 layout engine
- `--port PORT` bind to a specific local port
- `--browser BROWSER` open a specific browser command
- `--no-browser` start without opening a browser
- `--sketch` enable sketch mode

## Notes

- PNG export uses native `d2` rendering, so tooltips and other D2-specific export details come through there.
- SVG copy is exported as an SVG file object in the browser clipboard.
