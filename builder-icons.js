// The builder's icon set, drawn by hand for JukaHub. No icon font, no CDN.
//
// Every glyph sits on a 24 x 24 grid and is stroked at 2 units with round caps
// and joins in currentColor, so an icon takes the colour and size of the text
// around it (.ico in style.css). A few glyphs carry a filled accent (the play
// triangle, the favourite star, the AI sparkles, small dots) where an outline
// alone read too thin at 16 px.
//
// How it is used:
//  - index.html marks a spot with
//      <svg class="ico" data-icon="close" aria-hidden="true"></svg>
//    and hydrateIcons() fills it in when this file runs. It loads before
//    script.js, so the static chrome is drawn before the builder starts.
//  - script code builds markup with iconSvg(name) or an element with
//    createIcon(name).
// Both draw nothing for a name this file does not know (the box keeps its
// size), and tests/web/selfhost.test.mjs fails when markup or a script asks for
// such a name, so a typo cannot ship as a blank button.
//
// The canvas paints a tile's glyph from the player's icon names (script.js
// HOME_ICON_NAMES and TILE_ICON_NAMES map them onto these drawings); `media`,
// `apps`, `package`, `sliders`, `power` and `ring` follow the shapes
// player/homelayout.go paints for the same cards.
const BUILDER_ICONS = (() => {
  const circle = (cx, cy, r) => `<circle cx="${cx}" cy="${cy}" r="${r}"/>`;
  // A filled dot with no outline: buttons on a pad, key caps, paint wells.
  const dot = (cx, cy, r = 1.2) => `<circle cx="${cx}" cy="${cy}" r="${r}" fill="currentColor" stroke="none"/>`;
  const rect = (x, y, w, h, rx) => `<rect x="${x}" y="${y}" width="${w}" height="${h}" rx="${rx}"/>`;
  // Filled and outlined: the outline's round joins soften the corners.
  const fill = d => `<path d="${d}" fill="currentColor"/>`;
  // Filled only, for shapes that must not grow by the stroke.
  const solid = d => `<path d="${d}" fill="currentColor" stroke="none"/>`;
  // A four-point twinkle: quadratic curves pinched through the centre.
  const sparkle = (cx, cy, r) =>
    `M${cx} ${cy - r}Q${cx} ${cy} ${cx + r} ${cy}Q${cx} ${cy} ${cx} ${cy + r}Q${cx} ${cy} ${cx - r} ${cy}Q${cx} ${cy} ${cx} ${cy - r}Z`;
  // A page with a folded corner, shared by the file glyphs.
  const page = 'M14 3H7a2.5 2.5 0 0 0-2.5 2.5v13A2.5 2.5 0 0 0 7 21h10a2.5 2.5 0 0 0 2.5-2.5V8.5zM14 3v4a1.5 1.5 0 0 0 1.5 1.5h4';
  // A cloud's outline with its base left open, for the transfer arrows.
  const cloudArc = 'M7.5 17.5h-.75A4.25 4.25 0 0 1 6 9.07 6 6 0 0 1 17.92 8.05 4.75 4.75 0 0 1 17.25 17.5h-.75';

  // A string is path data; an array mixes path data and shapes.
  const icons = {
    // Actions
    close: 'M6 6l12 12M18 6 6 18',
    plus: 'M12 5v14M5 12h14',
    minus: 'M5 12h14',
    undo: 'M9 14.5 4 9.5l5-5M4 9.5h10.5a5 5 0 0 1 0 10H11',
    redo: 'M15 14.5l5-5-5-5M20 9.5H9.5a5 5 0 0 0 0 10H13',
    refresh: 'M3.5 12a8.5 8.5 0 0 1 14.5-6M18 2v4h-4M20.5 12a8.5 8.5 0 0 1-14.5 6M6 22v-4h4',
    upload: 'M12 15V4M7.5 8.5 12 4l4.5 4.5M4 15.5V18a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2.5',
    download: 'M12 4v11M7.5 10.5 12 15l4.5-4.5M4 15.5V18a2 2 0 0 0 2 2h12a2 2 0 0 0 2-2v-2.5',
    copy: [rect(9, 9, 12, 12, 2.25), 'M5.5 15H5a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v.5'],
    trash: 'M4 6.5h16M9.5 6.5v-2a1 1 0 0 1 1-1h3a1 1 0 0 1 1 1v2M6 6.5l.9 12.1a2 2 0 0 0 2 1.9h6.2a2 2 0 0 0 2-1.9L18 6.5M10 10.5v6M14 10.5v6',
    edit: [
      'M11 4.5H6A2.5 2.5 0 0 0 3.5 7v11A2.5 2.5 0 0 0 6 20.5h11a2.5 2.5 0 0 0 2.5-2.5v-5',
      'M17.86 3.66 9.76 11.76 9.37 14.63 12.24 14.24 20.34 6.14A1.75 1.75 0 0 0 17.86 3.66Z',
    ],
    pen: 'M17.77 3.27 6.02 15.02 5.66 18.34 8.98 17.98 20.73 6.23A2.1 2.1 0 0 0 17.77 3.27ZM15.36 5.67l2.97 2.97',
    eraser: [
      'M5.81 15.22a2 2 0 0 1 0-2.83l7.78-7.78a2 2 0 0 1 2.83 0l2.97 2.97a2 2 0 0 1 0 2.83l-7.78 7.78a2 2 0 0 1-2.83 0Z',
      'M8.78 9.42l5.8 5.8M10 20.5h10',
    ],
    broom: [
      'M19.5 4.5l-5.66 5.66',
      'M12.29 8.6 3.59 12.63l7.78 7.78 4.03-8.7Z',
      'M9.77 9.77l4.46 4.46M9.35 12.74l-3.18 2.47M11.26 14.65l-2.47 3.18',
    ],
    search: [circle(10.5, 10.5, 6.5), 'M15.1 15.1 20 20'],
    share: [circle(18, 5, 2.75), circle(6, 12, 2.75), circle(18, 19, 2.75), 'M8.59 10.49l6.82-3.98M8.59 13.51l6.82 3.98'],
    fork: [circle(6, 5, 2.5), circle(18, 5, 2.5), circle(12, 19, 2.5), 'M6 7.5V9a3 3 0 0 0 3 3h6a3 3 0 0 0 3-3V7.5M12 12v4.5'],
    external: 'M13.5 3.5h7v7M20.5 3.5 11 13M17.5 13.5V18a2.5 2.5 0 0 1-2.5 2.5H6A2.5 2.5 0 0 1 3.5 18V9A2.5 2.5 0 0 1 6 6.5h4.5',
    expand: 'M4 9.5V4h5.5M14.5 4H20v5.5M20 14.5V20h-5.5M9.5 20H4v-5.5',
    eye: ['M2.5 12C4.6 7.7 8 5.5 12 5.5s7.4 2.2 9.5 6.5c-2.1 4.3-5.5 6.5-9.5 6.5S4.6 16.3 2.5 12Z', circle(12, 12, 3)],
    click: ['M9 9l11 4-4.75 2.25L13 20Z', 'M9 2.5V5M2.5 9H5M4.4 4.4l1.8 1.8'],
    pointer: 'M5.5 3.5v15l4.1-3.9 2.8 6 2.6-1.2-2.7-5.9H18Z',
    bolt: 'M13.5 2.5 4.5 13.5h7l-1 8 9-11h-7Z',
    wand: ['M3.5 20.5l9.75-9.75', fill(sparkle(16.75, 7.25, 4.25)), solid(sparkle(20.5, 13.75, 2.25)), solid(sparkle(10, 3.25, 2.25))],

    // Navigation
    'chevron-left': 'M15 5.5 8.5 12l6.5 6.5',
    'chevron-right': 'M9 5.5l6.5 6.5L9 18.5',
    'chevron-down': 'M5.5 9l6.5 6.5L18.5 9',
    'caret-down': fill('M7 10h10l-5 5.5Z'),
    'arrow-up': 'M12 19.5v-15M6 10.5l6-6 6 6',
    'arrow-down': 'M12 4.5v15M6 13.5l6 6 6-6',
    back: 'M19.5 12h-15M10.5 6l-6 6 6 6',
    'arrows-h': 'M3.5 12h17M7.5 8l-4 4 4 4M16.5 8l4 4-4 4',
    'arrows-v': 'M12 3.5v17M8 7.5l4-4 4 4M8 16.5l4 4 4-4',
    menu: 'M4 6.5h16M4 12h16M4 17.5h16',

    // Status
    'check-circle': [circle(12, 12, 10), 'M8 12.4l2.8 2.8 5.4-5.6'],
    'alert-circle': [circle(12, 12, 10), 'M12 7.5v5.25', dot(12, 16.25)],
    warning: ['M10.08 6.44a2.2 2.2 0 0 1 3.84 0l5.75 10.29A2.2 2.2 0 0 1 17.75 20H6.25a2.2 2.2 0 0 1-1.92-3.27Z', 'M12 10v3.5', dot(12, 16.6, 1.1)],
    help: [circle(12, 12, 10), 'M9.31 9.03a2.75 2.75 0 1 1 4.38 2.74c-.9.75-1.69 1.13-1.69 2.23', dot(12, 17, 1.15)],
    bulb: ['M9.25 16.5c0-.8-.38-1.2-1.12-1.72a6.75 6.75 0 1 1 7.74 0c-.74.52-1.12.92-1.12 1.72Z', 'M9.75 19.75h4.5'],
    wifi: ['M8.71 15.43a4.5 4.5 0 0 1 6.58 0M5.42 12.36a9 9 0 0 1 13.16 0M2.13 9.29a13.5 13.5 0 0 1 19.74 0', dot(12, 18.75, 1.4)],
    sun: [circle(12, 12, 4), 'M12 2.5v2M12 19.5v2M2.5 12h2M19.5 12h2M5.28 5.28l1.42 1.42M17.3 17.3l1.42 1.42M5.28 18.72l1.42-1.42M17.3 6.7l1.42-1.42'],
    moon: 'M10.87 3.77A8.75 8.75 0 1 0 20.1 14.09 7 7 0 0 1 10.87 3.77Z',
    contrast: [circle(12, 12, 9), solid('M12 3a9 9 0 0 1 0 18Z')],
    power: 'M12 3v8M6.54 6.24a8.5 8.5 0 1 0 10.92 0',
    clock: [circle(12, 12, 10), 'M12 6.5V12l3.5 2'],
    // The player's unknown-icon fallback: a thick ring.
    ring: '<circle cx="12" cy="12" r="6" stroke-width="4"/>',

    // Editor
    grid: [rect(3.5, 3.5, 6.25, 6.25, 1.5), rect(14.25, 3.5, 6.25, 6.25, 1.5), rect(3.5, 14.25, 6.25, 6.25, 1.5), rect(14.25, 14.25, 6.25, 6.25, 1.5)],
    apps: [
      fill('M5.25 4h3.25a1.25 1.25 0 0 1 1.25 1.25V8.5A1.25 1.25 0 0 1 8.5 9.75H5.25A1.25 1.25 0 0 1 4 8.5V5.25A1.25 1.25 0 0 1 5.25 4Z'),
      fill('M15.5 4h3.25A1.25 1.25 0 0 1 20 5.25V8.5a1.25 1.25 0 0 1-1.25 1.25H15.5a1.25 1.25 0 0 1-1.25-1.25V5.25A1.25 1.25 0 0 1 15.5 4Z'),
      fill('M5.25 14.25h3.25a1.25 1.25 0 0 1 1.25 1.25v3.25A1.25 1.25 0 0 1 8.5 20H5.25A1.25 1.25 0 0 1 4 18.75V15.5a1.25 1.25 0 0 1 1.25-1.25Z'),
      fill('M15.5 14.25h3.25A1.25 1.25 0 0 1 20 15.5v3.25A1.25 1.25 0 0 1 18.75 20H15.5a1.25 1.25 0 0 1-1.25-1.25V15.5a1.25 1.25 0 0 1 1.25-1.25Z'),
    ],
    layers: 'M12 2.5l9 4.75-9 4.75-9-4.75ZM3 11.75l9 4.75 9-4.75M3 16.25 12 21l9-4.75',
    shapes: ['M12 3l4.5 7.5h-9Z', circle(7, 17, 3.75), rect(13.25, 13.25, 7.5, 7.5, 1.5)],
    button: [rect(2.5, 6.5, 19, 11, 3.5), 'M8.5 12h7'],
    type: 'M5 7V5h14v2M12 5v14M9.5 19h5',
    image: [rect(3, 4, 18, 16, 2.5), dot(8.75, 9.25, 1.75), 'M20.5 15 16 10.5 5 20'],
    'menu-bar': [rect(3, 4, 18, 16, 2.5), fill('M3 9.5V6.5A2.5 2.5 0 0 1 5.5 4h13A2.5 2.5 0 0 1 21 6.5v3Z')],
    list: [dot(4.75, 6.5, 1.25), dot(4.75, 12, 1.25), dot(4.75, 17.5, 1.25), 'M9 6.5h11M9 12h11M9 17.5h11'],
    'align-left': 'M4 6h16M4 10h10M4 14h16M4 18h10',
    columns: [rect(3.5, 4, 17, 16, 2.5), 'M12 4v16'],
    rows: [rect(3.5, 4, 17, 16, 2.5), 'M3.5 9.33h17M3.5 14.67h17'],
    sliders: ['M4 6h8.25M16.75 6H20M4 12h2.25M10.75 12H20M4 18h9.75M18.25 18H20', circle(14.5, 6, 2.25), circle(8.5, 12, 2.25), circle(16, 18, 2.25)],
    'text-size': 'M3.5 6.5V5h10v1.5M8.5 5v14M6.5 19h4M18 5.5v13M15.75 7.75 18 5.5l2.25 2.25M15.75 16.25 18 18.5l2.25-2.25',
    heading: 'M6 4.5v15M18 4.5v15M6 12h12M4 4.5h4M16 4.5h4M4 19.5h4M16 19.5h4',
    palette: [
      'M12 3a9 9 0 0 0 0 18c1.1 0 1.8-.8 1.8-1.8 0-.5-.2-.9-.5-1.3-.3-.3-.5-.8-.5-1.2 0-1 .8-1.7 1.8-1.7h1.9a4.5 4.5 0 0 0 4.5-4.5C21 6.5 17 3 12 3Z',
      dot(7.5, 11.5, 1.3), dot(9.5, 7.5, 1.3), dot(14, 6.75, 1.3), dot(17, 10, 1.3),
    ],
    fill: ['M12 3s-6.5 7-6.5 11.5a6.5 6.5 0 0 0 13 0C18.5 10 12 3 12 3Z', solid('M7.6 15.25h8.8a4.4 4.4 0 0 1-8.8 0Z')],
    code: 'M8 7.5 3.5 12 8 16.5M16 7.5l4.5 4.5-4.5 4.5M13.5 5l-3 14',
    braces: 'M9 3.5H8A2.5 2.5 0 0 0 5.5 6v3.5A2.5 2.5 0 0 1 3 12a2.5 2.5 0 0 1 2.5 2.5V18A2.5 2.5 0 0 0 8 20.5h1M15 3.5h1A2.5 2.5 0 0 1 18.5 6v3.5A2.5 2.5 0 0 0 21 12a2.5 2.5 0 0 0-2.5 2.5V18a2.5 2.5 0 0 1-2.5 2.5h-1',
    diagram: [circle(12, 5, 2.25), circle(6, 19, 2.25), circle(18, 19, 2.25), 'M12 7.25V12M6 16.75V14a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v2.75'],
    terminal: [rect(2.5, 4, 19, 16, 2.5), 'M7 9.5l3 3-3 3M12.5 15.5H17'],
    keyboard: [rect(2, 5.5, 20, 13, 2.5), dot(6.75, 9.5, 1.05), dot(10.25, 9.5, 1.05), dot(13.75, 9.5, 1.05), dot(17.25, 9.5, 1.05), 'M7.5 14.5h9'],
    user: [circle(12, 8, 4.25), 'M4.5 20.5v-.75a6.25 6.25 0 0 1 6.25-6.25h2.5a6.25 6.25 0 0 1 6.25 6.25v.75'],
    // A TrimUI-style handheld: lit screen between the D-pad and the face buttons.
    handheld: [
      rect(1.5, 6, 21, 12, 4),
      solid('M8.25 8.5h7.5A1.25 1.25 0 0 1 17 9.75v4.5a1.25 1.25 0 0 1-1.25 1.25h-7.5A1.25 1.25 0 0 1 7 14.25v-4.5A1.25 1.25 0 0 1 8.25 8.5Z'),
      dot(4.6, 12, 1.35), dot(18.9, 10.9, 1), dot(19.9, 13.1, 1),
    ],
    plug: 'M9.5 3v5M14.5 3v5M7 8h10v3a5 5 0 0 1-10 0ZM12 16v5.5',
    mic: [rect(9, 2.5, 6, 11.5, 3), 'M5.5 11a6.5 6.5 0 0 0 13 0M12 17.5V21M8.5 21h7'],
    monitor: [rect(2.5, 3.5, 19, 13, 2.5), 'M12 16.5v4M8 20.5h8'],
    clipboard: ['M8.5 4.5H7a2 2 0 0 0-2 2v13a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2v-13a2 2 0 0 0-2-2h-1.5', rect(8.5, 2.5, 7, 4, 1.25), 'M9 11.5h6M9 15.5h4'],
    file: page,
    'file-code': [page, 'M10 12.5l-2 2 2 2M14 12.5l2 2-2 2'],
    logs: [page, 'M8.5 12.5h7M8.5 16.5h7'],
    folder: 'M3 6.5a2 2 0 0 1 2-2h4.2a1.5 1.5 0 0 1 1.2.6L12 7h7a2 2 0 0 1 2 2v8.5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2Z',
    disk: ['M3 14l2.4-7.4A2.25 2.25 0 0 1 7.55 5h8.9a2.25 2.25 0 0 1 2.15 1.6L21 14', rect(3, 14, 18, 6, 2), dot(7, 17, 1), dot(10.5, 17, 1)],

    // Apps and media: the canvas tile glyphs
    media: [rect(2.5, 4, 19, 16, 3.5), fill('M10 8.75v6.5L15.5 12Z')],
    play: fill('M8 4.5v15L20 12Z'),
    'media-stack': [rect(2.5, 7.5, 14, 12, 2), fill('M8 11v5l4.25-2.5Z'), 'M6.5 4.5h13a2 2 0 0 1 2 2V15'],
    gamepad: [
      'M6.5 6h11c2.5 0 3.9 1.9 4.3 4.3l.7 4.8c.3 2-.9 3.4-2.5 3.4-1.1 0-1.9-.6-2.4-1.6l-.8-1.6c-.3-.5-.8-.8-1.4-.8H8.6c-.6 0-1.1.3-1.4.8l-.8 1.6c-.5 1-1.3 1.6-2.4 1.6-1.6 0-2.8-1.4-2.5-3.4l.7-4.8C2.6 7.9 4 6 6.5 6Z',
      'M6.5 9v4M4.5 11h4', dot(15.75, 10, 1.15), dot(17.75, 12.25, 1.15),
    ],
    chat: 'M7.5 18 3.5 21V7a3 3 0 0 1 3-3h11a3 3 0 0 1 3 3v8a3 3 0 0 1-3 3Z',
    star: fill('M12 3.14l2.88 5.83 6.44.94-4.66 4.54 1.1 6.41L12 17.84l-5.76 3.02 1.1-6.41-4.66-4.54 6.44-.94Z'),
    globe: [circle(12, 12, 9.5), 'M12 2.5c-5.33 5.33-5.33 13.67 0 19M12 2.5c5.33 5.33 5.33 13.67 0 19M2.5 12h19'],
    package: 'M12 2.8l8.5 4.6v9.2L12 21.2l-8.5-4.6V7.4ZM3.8 7.6 12 12l8.2-4.4M12 12v9M7.75 5.1l8.5 4.6',
    settings: [
      'M9.56 5.12 10.01 2.2A10 10 0 0 1 13.99 2.2L14.44 5.12A7.3 7.3 0 0 1 16.74 6.45L19.49 5.37A10 10 0 0 1 21.48 8.83L19.18 10.67A7.3 7.3 0 0 1 19.18 13.33L21.48 15.17A10 10 0 0 1 19.49 18.63L16.74 17.55A7.3 7.3 0 0 1 14.44 18.88L13.99 21.8A10 10 0 0 1 10.01 21.8L9.56 18.88A7.3 7.3 0 0 1 7.26 17.55L4.51 18.63A10 10 0 0 1 2.52 15.17L4.82 13.33A7.3 7.3 0 0 1 4.82 10.67L2.52 8.83A10 10 0 0 1 4.51 5.37L7.26 6.45A7.3 7.3 0 0 1 9.56 5.12Z',
      circle(12, 12, 3),
    ],
    tv: [rect(2.5, 7, 19, 13, 2.5), 'M8 3l4 4 4-4'],
    cloud: 'M17.25 19H6.75A4.25 4.25 0 0 1 6 10.57 6 6 0 0 1 17.92 9.55 4.75 4.75 0 0 1 17.25 19Z',
    'cloud-up': [cloudArc, 'M12 21v-9M8.75 15.25 12 12l3.25 3.25'],
    'cloud-down': [cloudArc, 'M12 11.5v9.5M8.75 17.75 12 21l3.25-3.25'],
    chart: 'M4 4v14.5A1.5 1.5 0 0 0 5.5 20H20M8 15l3.5-4 3 2.5 5-5.5',
    cpu: [
      rect(6, 6, 12, 12, 2),
      fill('M10.25 9.5h3.5a.75.75 0 0 1 .75.75v3.5a.75.75 0 0 1-.75.75h-3.5a.75.75 0 0 1-.75-.75v-3.5a.75.75 0 0 1 .75-.75Z'),
      'M9.5 2.5V6M14.5 2.5V6M9.5 18v3.5M14.5 18v3.5M2.5 9.5H6M2.5 14.5H6M18 9.5h3.5M18 14.5h3.5',
    ],
    speed: ['M3.84 17.3A9 9 0 1 1 20.16 17.3', 'M12 13.5 16 9', dot(12, 13.5, 1.6)],
    network: [rect(9, 2.5, 6, 5, 1.25), rect(3, 16.5, 6, 5, 1.25), rect(15, 16.5, 6, 5, 1.25), 'M12 7.5V12M6 16.5V14a2 2 0 0 1 2-2h8a2 2 0 0 1 2 2v2.5'],
  };

  const out = Object.create(null);
  for (const name of Object.keys(icons)) {
    out[name] = [].concat(icons[name])
      .map(part => (part.charAt(0) === '<' ? part : `<path d="${part}"/>`))
      .join('');
  }
  return Object.freeze(out);
})();

// The presentation every icon shares. Attributes rather than CSS so a glyph
// draws the same wherever it lands, including inside the canvas tiles. Icons
// are decorative: the control around one carries the accessible name.
const BUILDER_ICON_ATTRS = Object.freeze({
  viewBox: '0 0 24 24',
  fill: 'none',
  stroke: 'currentColor',
  'stroke-width': '2',
  'stroke-linecap': 'round',
  'stroke-linejoin': 'round',
  'aria-hidden': 'true',
  focusable: 'false',
});

function builderIconKnown(name) {
  if (typeof name === 'string' && Object.prototype.hasOwnProperty.call(BUILDER_ICONS, name)) return true;
  // Said once per name, so a typo shows in the console (and fails the jsdom
  // suite) without flooding it on every repaint.
  const key = String(name);
  if (!builderIconKnown.reported.has(key)) {
    builderIconKnown.reported.add(key);
    console.warn(`builder-icons: no icon named "${key}"`);
  }
  return false;
}
builderIconKnown.reported = new Set();

// iconSvg returns the markup for one icon. className adds classes after "ico"
// (letters, digits, dashes and spaces only), so a caller can hand the result
// to innerHTML without escaping.
function iconSvg(name, className) {
  const known = builderIconKnown(name);
  const extra = String(className || '').replace(/[^\w -]/g, '').trim();
  const attrs = Object.keys(BUILDER_ICON_ATTRS).map(attr => `${attr}="${BUILDER_ICON_ATTRS[attr]}"`).join(' ');
  return `<svg class="ico${extra ? ' ' + extra : ''}"${known ? ` data-icon="${name}"` : ''} ${attrs}>` +
    (known ? BUILDER_ICONS[name] : '') + '</svg>';
}

// createIcon is iconSvg as an element, for code that builds DOM nodes.
function createIcon(name, className) {
  const holder = document.createElement('template');
  holder.innerHTML = iconSvg(name, className);
  return holder.content.firstElementChild;
}

// hydrateIcons draws every empty <svg data-icon="..."> placeholder under root.
function hydrateIcons(root) {
  const scope = root || document;
  scope.querySelectorAll('svg[data-icon]').forEach(svg => {
    const name = svg.getAttribute('data-icon');
    if (svg.firstElementChild || !builderIconKnown(name)) return;
    for (const attr of Object.keys(BUILDER_ICON_ATTRS)) {
      if (!svg.hasAttribute(attr)) svg.setAttribute(attr, BUILDER_ICON_ATTRS[attr]);
    }
    svg.classList.add('ico');
    svg.innerHTML = BUILDER_ICONS[name];
  });
}

if (typeof document !== 'undefined') hydrateIcons(document);
