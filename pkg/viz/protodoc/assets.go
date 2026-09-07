// Copyright (c) 2026 JAAB Tech SAS, Uruguay
// SPDX-License-Identifier: Apache-2.0

package protodoc

// The page's stylesheet, inline. A protocol reference is mailed, attached to a
// ticket and opened offline, so nothing may be fetched: a page that needs the
// network is a page that renders wrong exactly when someone needs it.
//
// The palette is neutral on purpose. Whoever generates a reference for their own
// protocol should not find another company's colours on it.
const pageCSS = `
:root{
  --ground:#FFF; --panel:#FFF; --raise:#F6F7F8; --ink:#1B1F23; --muted:#606A73;
  --rule:#DFE3E6; --rule-soft:#EDF0F2; --accent:#2A6F7B; --accent-soft:#E6F0F2;
  --mand-bg:#2A6F7B; --mand-fg:#FFF; --forb:#9A3B3F; --forb-bg:#FAEDED;
  --echo:#2A6F7B; --new:#4A5C93; --mod:#8A6520; --cond:#5A6470;
}
@media (prefers-color-scheme:dark){
  :root:not([data-theme="light"]){
    --ground:#16191B; --panel:#1D2123; --raise:#22272A; --ink:#E4E8EA; --muted:#95A0A7;
    --rule:#30363A; --rule-soft:#272C2F; --accent:#5CBBC9; --accent-soft:#16333A;
    --mand-bg:#5CBBC9; --mand-fg:#16191B; --forb:#E09499; --forb-bg:#382527;
    --echo:#5CBBC9; --new:#9AA7DF; --mod:#D2A75C; --cond:#A8B2BA;
  }
}
:root[data-theme="dark"]{
  --ground:#16191B; --panel:#1D2123; --raise:#22272A; --ink:#E4E8EA; --muted:#95A0A7;
  --rule:#30363A; --rule-soft:#272C2F; --accent:#5CBBC9; --accent-soft:#16333A;
  --mand-bg:#5CBBC9; --mand-fg:#16191B; --forb:#E09499; --forb-bg:#382527;
  --echo:#5CBBC9; --new:#9AA7DF; --mod:#D2A75C; --cond:#A8B2BA;
}
*{box-sizing:border-box}
body{
  margin:0;background:var(--ground);color:var(--ink);font-size:15px;line-height:1.62;
  font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;
  -webkit-font-smoothing:antialiased;
}
code,.mono{font-family:ui-monospace,SFMono-Regular,"SF Mono",Menlo,Consolas,monospace;font-size:.88em}
.wrap{max-width:1180px;margin:0 auto;padding:0 26px}
header{background:var(--panel);border-bottom:1px solid var(--rule);padding:24px 0 20px}
.hrow{display:flex;gap:20px;align-items:flex-end;justify-content:space-between;flex-wrap:wrap}
.hbtns{display:flex;gap:9px}
.eyebrow{margin:0 0 7px;font-size:11px;letter-spacing:.1em;text-transform:uppercase;color:var(--muted)}
h1{margin:0 0 5px;font-size:25px;line-height:1.2;letter-spacing:-.02em;text-wrap:balance}
.sub{margin:0;color:var(--muted);font-size:13.5px}
.ver{margin:3px 0 0;color:var(--muted);font-size:12px;font-variant-numeric:tabular-nums}
.iconbtn{
  display:inline-flex;align-items:center;justify-content:center;width:36px;height:36px;padding:0;
  border:1px solid var(--rule);border-radius:8px;background:var(--raise);color:var(--muted);cursor:pointer;
}
.iconbtn:hover{color:var(--accent);border-color:var(--accent)}
.iconbtn:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.navclose{display:none}
.iconbtn .moon{display:none}
:root[data-theme="dark"] .iconbtn .sun{display:none}
:root[data-theme="dark"] .iconbtn .moon{display:inline}
@media (prefers-color-scheme:dark){
  :root:not([data-theme="light"]) .iconbtn .sun{display:none}
  :root:not([data-theme="light"]) .iconbtn .moon{display:inline}
}
/* The index sits on the right. The document is what the reader came for, so it
   starts where their eye does; and the panel already slides in from the right on
   a narrow screen, so the two now agree about which side it lives on.
   Both are placed explicitly rather than reordered in the markup: the index
   stays first in the document, which is where a reader not using the layout --
   a screen reader, a printer -- wants it. */
.layout{display:grid;grid-template-columns:minmax(0,1fr) 232px;gap:36px;padding-top:26px;padding-bottom:70px}
nav{grid-column:2;grid-row:1;position:sticky;top:66px;align-self:start;max-height:calc(100vh - 90px);overflow-y:auto;font-size:13px}
nav .grp{margin:18px 0 7px;font-size:10.5px;letter-spacing:.1em;text-transform:uppercase;color:var(--muted);font-weight:600}
nav .grp:first-child{margin-top:0}
nav a{display:flex;gap:9px;padding:2.5px 0 2.5px 10px;margin-left:-12px;border-left:2px solid transparent;text-decoration:none;color:var(--ink)}
nav a:hover{border-left-color:var(--accent);color:var(--accent)}
/* Where the reader is, which hovering cannot say: the pointer is wherever the
   hand left it, and often nowhere near the section on screen. The two states
   have to be told apart, so this one fills rather than just colouring. */
nav a[aria-current="location"]{
  border-left-color:var(--accent);color:var(--accent);font-weight:600;
  background:var(--accent-soft);border-radius:0 4px 4px 0;padding-right:8px;
}
nav a:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
nav a .k{flex:0 0 auto;min-width:30px;color:var(--muted);font-family:ui-monospace,Menlo,monospace;font-size:11.5px}
nav a .v{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
main{grid-column:1;grid-row:1;min-width:0}
h2{margin:46px 0 16px;padding-bottom:9px;border-bottom:1px solid var(--rule);font-size:11.5px;letter-spacing:.11em;text-transform:uppercase;color:var(--muted)}
h2:first-child{margin-top:0}
h3{margin:32px 0 10px;font-size:17.5px;letter-spacing:-.01em;scroll-margin-top:66px}
p{margin:0 0 12px;max-width:68ch}
.pair{color:var(--muted)}
.lbl{margin:16px 0 8px}
ul.facts{margin:0 0 15px;padding-left:19px;max-width:68ch}
ul.facts.wire{
  display:flex;flex-wrap:wrap;gap:5px 20px;list-style:none;padding:9px 13px;margin:0 0 13px;
  border:1px solid var(--rule);border-radius:8px;background:var(--raise);font-size:13px;
}
ul.facts.wire li{margin:0}
ul.facts.wire strong{color:var(--muted);font-weight:600;font-size:11px;letter-spacing:.05em;text-transform:uppercase;margin-right:5px}
ul.facts li{margin:3px 0}
blockquote{margin:0 0 14px;padding:10px 15px;max-width:68ch;border-left:3px solid var(--accent);background:var(--raise);border-radius:0 7px 7px 0}
code{padding:1px 5px;border:1px solid var(--rule);border-radius:4px;background:var(--rule-soft)}

/* The text a section was derived from, folded away. Open by choice: a reference
   is read for its tables, and the source is there for the reader who doubts one. */
/* The source controls sit with the title they explain. */
/* The spec, coloured and folded. A thousand lines in one block is a block
   nobody reads. */
.yaml{
  border:1px solid var(--rule);border-radius:9px;background:var(--panel);
  padding:12px 14px;overflow-x:auto;
  font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;
  font-size:12.5px;line-height:1.55;white-space:pre;
}
.yaml .yl{white-space:pre}
/* The number is a gutter, not content: copying the block has to yield YAML and
   not a column of digits down its left. */
.yaml .yn{
  display:inline-block;min-width:3.4em;padding-right:1.1em;text-align:right;
  color:var(--muted);opacity:.55;-webkit-user-select:none;user-select:none;
}
.yaml.inline{border:0;border-radius:0;padding:11px 13px;background:var(--panel)}
.yaml.inline .yn{min-width:3em}
.yaml.nonum .yn{display:none}
.yc-type{border-bottom:1px dotted var(--muted);color:var(--ink);text-decoration:none}
.yc-type:hover{color:var(--accent);border-bottom-color:var(--accent)}
.yaml details.yf{margin:0}
.yaml details.yf>summary{
  list-style:none;cursor:pointer;white-space:pre;border-radius:3px;
}
.yaml details.yf>summary::-webkit-details-marker{display:none}
.yaml details.yf>summary:hover{background:var(--raise)}
.yaml details.yf>summary::after{content:" ▸";color:var(--muted);font-size:.85em}
.yaml details.yf[open]>summary::after{content:" ▾"}
.yaml .yc{display:block}
.yc-key{color:var(--accent);font-weight:600}
.yc-str{color:#0F7A4D}
.yc-num{color:#8A5A1B}
.yc-bool{color:#7A3E9D}
.yc-flow{color:#0F7A4D}
.yc-plain{color:var(--ink)}
.yc-com{color:var(--muted);font-style:italic}
.yc-punct,.yc-dash{color:var(--muted)}
:root[data-theme="dark"] .yc-str,:root[data-theme="dark"] .yc-flow{color:#6FD39B}
:root[data-theme="dark"] .yc-num{color:#D9A45B}
:root[data-theme="dark"] .yc-bool{color:#C9A0E8}
@media (prefers-color-scheme:dark){
  :root:not([data-theme="light"]) .yc-str,:root:not([data-theme="light"]) .yc-flow{color:#6FD39B}
  :root:not([data-theme="light"]) .yc-num{color:#D9A45B}
  :root:not([data-theme="light"]) .yc-bool{color:#C9A0E8}
}

.head{display:flex;align-items:baseline;gap:12px;flex-wrap:wrap;margin:32px 0 10px}
.head h3{margin:0}
.heads{display:flex;gap:6px}
.srcbtn{
  padding:2px 9px;border:1px solid var(--rule);border-radius:11px;background:var(--raise);
  color:var(--muted);font-family:ui-monospace,Menlo,monospace;font-size:10.5px;cursor:pointer;
}
.srcbtn:hover{color:var(--accent);border-color:var(--accent)}
.srcbtn[aria-expanded="true"]{background:var(--accent);color:var(--panel);border-color:var(--accent)}
.srcbtn:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
.srcpanel .cap{margin:0 0 10px;color:var(--muted);font-size:12px}
.srcpanel.term-def .grp{
  margin:0 0 6px;font-size:10.5px;letter-spacing:.09em;text-transform:uppercase;color:var(--muted);
}
.srcpanel.term-def p{max-width:64ch}
.srcpanel.term-def .cap{margin:14px 0 0}
.srcpanel.term-def .cap a{color:var(--accent)}
.srcpanel pre{margin:0;padding:11px 13px;overflow-x:auto;background:var(--panel);border:1px solid var(--rule);border-radius:7px}
.srcpanel code{border:0;padding:0;background:transparent;font-size:12px;line-height:1.5;white-space:pre}

/* One dialog, and the fragment is copied into it. Inline, each was a box under
   its element, and sixty boxes between a reader and the next element. */
dialog#srcdlg{
  width:min(880px,94vw);max-height:86vh;padding:0;border:1px solid var(--rule);border-radius:11px;
  background:var(--panel);color:var(--ink);overflow:hidden;
}
dialog#srcdlg::backdrop{background:rgba(0,0,0,.45)}
.dlghead{
  display:flex;align-items:center;justify-content:space-between;gap:14px;
  padding:13px 16px;border-bottom:1px solid var(--rule);background:var(--raise);
}
.dlghead strong{font-size:14.5px;font-weight:600}
#srcdlgbody{padding:16px;overflow:auto;max-height:calc(86vh - 56px)}
#srcdlgbody .yaml.inline{border:1px solid var(--rule);border-radius:7px}

details.src{margin:0 0 16px;border:1px solid var(--rule);border-radius:8px;background:var(--raise);max-width:100%}
details.src>summary{
  padding:7px 13px;cursor:pointer;color:var(--muted);
  font-family:ui-monospace,Menlo,monospace;font-size:11.5px;list-style:none;
}
details.src>summary::-webkit-details-marker{display:none}
details.src>summary::before{content:"▸ ";display:inline-block;width:1em}
details.src[open]>summary::before{content:"▾ "}
details.src>summary:hover{color:var(--accent)}
details.src>summary:focus-visible{outline:2px solid var(--accent);outline-offset:-2px}
details.src pre{
  margin:0;padding:11px 13px;overflow-x:auto;border-top:1px solid var(--rule);
  background:var(--panel);border-radius:0 0 7px 7px;
}
details.src code{border:0;padding:0;background:transparent;font-size:12px;line-height:1.5;white-space:pre}


/* A word the reference defines. The dotted rule is the affordance; the link is
   what answers on a phone, where nothing hovers. */
a.term{text-decoration:none;color:inherit;border-bottom:0}
a.term.plain{border-bottom:1px dotted var(--muted)}
a.term:hover .chip{border-color:var(--accent)}
a.term.plain:hover{border-bottom-color:var(--accent);color:var(--accent)}
a.term:focus-visible{outline:2px solid var(--accent);outline-offset:2px;border-radius:3px}

nav a.sub{padding-left:26px;font-size:12.5px}
nav a.sub .k{display:none}
nav [data-pane][hidden]{display:none}
h3.helpg{font-size:12px;letter-spacing:.09em;text-transform:uppercase;color:var(--muted);margin:22px 0 8px}
dl.help{margin:0 0 6px;max-width:74ch}
dl.help dt{margin:11px 0 3px;scroll-margin-top:24px}
dl.help dt code{font-size:13px}
dl.help dd{margin:0;color:var(--ink)}
dl.help dt:target code{background:var(--accent);color:var(--panel);border-color:var(--accent)}
/* Tabs. A reference of sixty elements read as one page is a page nobody
   navigates; each pane is a section, and print shows them all. */
/* The tabs stay put. A reference is scrolled a long way down, and a reader who
   has to return to the top to change section is a reader who does not. */
.tabbar{
  position:sticky;top:0;z-index:30;
  border-bottom:1px solid var(--rule);background:var(--panel);
}
.tabrow{display:flex;align-items:stretch;gap:14px;padding:0 26px}
/* Once the header has scrolled away, the mark in the bar is the only way back
   still on screen. */
/* At the top the header already carries the mark; a second one beside it is the
   same thing twice. It appears when the header has scrolled away. */
.tabhome{display:flex;align-items:center;flex:0 0 auto;padding:6px 0;
  opacity:0;visibility:hidden;width:0;overflow:hidden;transition:opacity .15s ease}
.tabhome.on{opacity:1;visibility:visible;width:auto}
.tabhome .logo{height:17px}
.tabs{display:flex;gap:2px;overflow-x:auto;scrollbar-width:none;min-width:0}
.tabs::-webkit-scrollbar{display:none}
.tab{
  flex:0 0 auto;padding:11px 15px;border:0;border-bottom:2px solid transparent;background:transparent;
  color:var(--muted);font:inherit;font-size:13.5px;cursor:pointer;white-space:nowrap;
}
.tab:hover{color:var(--ink)}
.tab[aria-selected="true"]{color:var(--accent);border-bottom-color:var(--accent);font-weight:600}
.tab:focus-visible{outline:2px solid var(--accent);outline-offset:-2px}
.pane[hidden]{display:none}
.pane>h2:first-child{margin-top:0}

a.xref{color:inherit;text-decoration:none;border-bottom:1px dotted var(--muted)}
a.xref:hover{color:var(--accent);border-bottom-color:var(--accent)}
a.xref:focus-visible{outline:2px solid var(--accent);outline-offset:2px;border-radius:3px}
td.n a.xref{border-bottom:0}
h3:target,dl.help dt:target,.head{scroll-margin-top:66px}
dl.help dt{scroll-margin-top:66px}

details.more{margin:0 0 16px;max-width:68ch}
details.more>summary{
  cursor:pointer;color:var(--accent);font-size:13.5px;list-style:none;padding:2px 0;
}
details.more>summary::-webkit-details-marker{display:none}
details.more>summary::before{content:"▸ "}
details.more[open]>summary::before{content:"▾ "}
details.more[open]>summary{margin-bottom:8px}
details.more>summary:focus-visible{outline:2px solid var(--accent);outline-offset:2px;border-radius:3px}


ul.refs{margin:0 0 16px;padding-left:19px;max-width:74ch}
ul.refs li{margin:0 0 9px}
.muted{color:var(--muted);font-size:.92em}
.tw{margin:0 0 20px;overflow-x:auto;border:1px solid var(--rule);border-radius:9px;background:var(--panel)}
table{width:100%;min-width:520px;border-collapse:collapse;font-size:13.5px}
th{padding:10px 14px;text-align:left;font-size:10.5px;letter-spacing:.09em;text-transform:uppercase;color:var(--muted);background:var(--raise);border-bottom:1px solid var(--rule);white-space:nowrap}
td{padding:9px 14px;border-bottom:1px solid var(--rule-soft);vertical-align:top}
tr:last-child td{border-bottom:0}
td br{content:"";display:block;margin-bottom:3px}
th.n,td.n{text-align:right;font-family:ui-monospace,Menlo,monospace;font-variant-numeric:tabular-nums;color:var(--muted)}
.nil{color:var(--muted);opacity:.45}
/* The first two columns identify a row. If they scroll away, everything left of
   the fold has no subject. */
th:first-child,td:first-child{position:sticky;left:0;z-index:2;background:var(--panel)}
th:nth-child(2),td:nth-child(2){position:sticky;left:52px;z-index:2;background:var(--panel);box-shadow:1px 0 0 var(--rule)}
th:first-child,th:nth-child(2){background:var(--raise)}
th:first-child{min-width:52px}
.chip{display:inline-block;padding:2px 9px;border:1px solid transparent;border-radius:12px;font-family:ui-monospace,Menlo,monospace;font-size:11px;white-space:nowrap}
.u-mandatory{background:var(--mand-bg);color:var(--mand-fg)}
.u-optional{color:var(--muted);border-color:var(--rule)}
.u-forbidden{background:var(--forb-bg);color:var(--forb);border-color:var(--forb)}
.u-conditional{color:var(--cond);border-color:var(--cond)}
.r-echo{color:var(--echo);border-color:var(--echo)}
.r-new{color:var(--new);border-color:var(--new)}
.r-modified{color:var(--mod);border-color:var(--mod)}
.r-varies{color:var(--muted);border-color:var(--rule)}
footer{border-top:1px solid var(--rule);background:var(--panel);padding:22px 0 40px;color:var(--muted);font-size:12.5px}
footer p{max-width:76ch;margin:0}

/* The mark of what generated the page.
   The wordmark's colours travel as custom properties rather than as rules: the
   mark is drawn through <use>, and ordinary CSS does not cross that boundary —
   which left it a single flat colour. Custom properties inherit through it. */
.logo{height:22px;width:auto;flex:0 0 auto}
/* The wordmark's dark blue vanishes on a dark ground, and a fill attribute is
   not reachable any other way. */
:root[data-theme="dark"] .logo{--logo-word:#7FB3CC}
@media (prefers-color-scheme:dark){:root:not([data-theme="light"]) .logo{--logo-word:#7FB3CC}}
.brand{display:flex;align-items:center;gap:13px;margin-bottom:11px}
a.home{display:inline-flex;line-height:0;border-radius:4px}
a.home:focus-visible{outline:2px solid var(--accent);outline-offset:3px}
.brand .bar{width:1px;height:22px;background:var(--rule)}

/* Two floating controls. A reference is scrolled a long way down, and the way
   back has to be one reach rather than a swipe past sixty elements. */
.float{position:fixed;right:18px;bottom:18px;display:flex;flex-direction:column;gap:9px;z-index:40}
.float button{
  display:flex;align-items:center;justify-content:center;width:42px;height:42px;padding:0;
  border:1px solid var(--rule);border-radius:50%;background:var(--panel);color:var(--muted);
  cursor:pointer;box-shadow:0 2px 10px rgba(0,0,0,.13);
}
.float button:hover{color:var(--accent);border-color:var(--accent)}
.float button:focus-visible{outline:2px solid var(--accent);outline-offset:2px}
#top{opacity:0;visibility:hidden;transition:opacity .18s ease,visibility .18s ease}
#top.on{opacity:1;visibility:visible}
#menu{display:none}
.navclose{display:none}
@media (max-width:880px){
  .layout{grid-template-columns:1fr;gap:0}
  main{grid-column:1}
  #menu{display:flex}
  /* The index is a panel here. Above the content it would be a wall of sixty
     entries to scroll past before reaching what it points at. */
  nav{
    grid-column:1;
    position:fixed;inset:0 0 0 auto;width:min(310px,86vw);z-index:50;
    max-height:none;height:100%;padding:20px 22px;margin:0;
    overflow-y:auto;-webkit-overflow-scrolling:touch;
    background:var(--panel);border-left:1px solid var(--rule);border-bottom:0;
    transform:translateX(100%);transition:transform .2s ease;
    /* Reaching the end of the index must not hand the scroll to the page behind
       it: the reader loses their place in the document they opened this to
       navigate. */
    overscroll-behavior:contain;
  }
  nav.open{transform:translateX(0)}
  .navclose{display:flex;position:absolute;top:14px;right:16px}
  .scrim{position:fixed;inset:0;background:rgba(0,0,0,.42);z-index:45;display:none}
  .scrim.on{display:block}
}
/* Holding the page still while the panel is open. Only the scrolling element is
   locked and nothing is repositioned: fixing the body sends the reader to the
   top of the document, and it takes the panel's own scroll with it. */
:root.navopen{overflow:hidden}
:root.navopen body{overflow:hidden}
@media (min-width:881px){.scrim{display:none}}

.printonly{display:none}

@media print{
  /* A PDF is read on paper. The reader's theme does not travel with it, a table
     split across sheets loses its header, and the browser prints the file's path
     where a document would print its own name. */
  :root,:root[data-theme="dark"],:root:not([data-theme="light"]){
    --ground:#FFF;--panel:#FFF;--raise:#FFF;--ink:#1A1A1A;--muted:#4A4A4A;
    --rule:#9A9A9A;--rule-soft:#CFCFCF;--accent:#2A6F7B;--mand-bg:#FFF;--mand-fg:#1A1A1A;
    --forb:#8C2F39;--forb-bg:#FFF;--echo:#2A6F7B;--new:#4A5C93;--mod:#7A5A1B;--cond:#444;
  }
  @page{margin:14mm 12mm}
  body{font-size:9pt;line-height:1.35}
  nav,.iconbtn{display:none!important}
  .float,.scrim,.hbtns{display:none!important}
  .printonly{display:block}
  footer .printonly{margin-top:6pt;font-size:7.5pt;letter-spacing:.04em;text-transform:uppercase}
  /* A printed reference has no sidebar and no search: without a contents page a
     reader looking for DE 39 turns pages until they find it. */
  .toc{page-break-after:always;break-after:page}
  .toc .tocg{margin:9pt 0 4pt;font-size:8pt;letter-spacing:.09em;text-transform:uppercase;color:var(--muted)}
  ul.toclist{columns:3;column-gap:10mm;margin:0 0 6pt;padding:0;list-style:none;font-size:8pt}
  ul.toclist li{break-inside:avoid;margin:0 0 1pt;padding-left:26pt;text-indent:-26pt}
  ul.toclist .k{display:inline-block;width:22pt;color:var(--muted);font-family:ui-monospace,Menlo,monospace}
  /* A PDF has no tabs: every pane prints, and each starts its own page. */
  .tabs,.tabbar{display:none!important;position:static}
  .pane[hidden]{display:block!important}
  .pane{page-break-before:always;break-before:page}
  .tabhome{display:none!important}
  .pane:first-of-type{page-break-before:avoid;break-before:auto}
  .logo{--logo-word:#2f627d}
  a.term,a.xref{border-bottom:0!important}
  dl.help{max-width:none}
  dl.help dt{margin:6pt 0 2pt}
  dl.help dd{page-break-inside:avoid;break-inside:avoid}
  h3.helpg{margin:12pt 0 4pt;font-size:8pt}
  ul.refs{padding-left:12pt}
  ul.refs li{margin-bottom:5pt;page-break-inside:avoid;break-inside:avoid}
  .layout{display:block;padding:0}
  .wrap{max-width:none;padding:0}

  header{padding:0 0 9pt;border-bottom:1.2pt solid var(--accent)}
  .logo{height:15pt}
  .brand{margin-bottom:7pt;gap:9pt}
  h1{font-size:17pt;margin-bottom:3pt}
  .sub,.ver{font-size:8.5pt}

  h2{margin:16pt 0 6pt;padding-bottom:4pt;font-size:8.5pt;page-break-after:avoid;break-after:avoid}
  /* A heading at the foot of a sheet with its table overleaf is a heading for
     nothing. */
  h3{margin:11pt 0 4pt;font-size:11pt;page-break-after:avoid;break-after:avoid}
  p,ul.facts{margin-bottom:6pt;max-width:none}
  .lbl{margin:8pt 0 4pt}
  blockquote{margin-bottom:8pt;padding:5pt 8pt;border-left-width:2pt;page-break-inside:avoid;break-inside:avoid}

  .tw{overflow:visible;border:0;margin-bottom:10pt;border-radius:0}
  table{min-width:0;font-size:8pt}
  thead{display:table-header-group}   /* the header repeats on every sheet */
  tr{page-break-inside:avoid;break-inside:avoid}
  th{padding:3.5pt 5pt;font-size:7pt;background:#EFEFEF!important;-webkit-print-color-adjust:exact;print-color-adjust:exact}
  td{padding:3pt 5pt}
  th,td{border:.4pt solid var(--rule);position:static!important;box-shadow:none!important}

  /* On paper a bordered pill around every cell is noise. What has to survive is
     the distinction between the four usages, and weight carries it. */
  .chip{border:0;padding:0;background:transparent!important;font-size:8pt}
  .u-mandatory{font-weight:700}
  .u-forbidden{font-weight:700;font-style:italic}
  .u-conditional{font-style:italic}
  .u-optional{color:var(--muted)}
  .r-echo,.r-new,.r-modified,.r-varies{color:var(--ink)}

  code{border:0;padding:0;background:transparent!important;font-size:7.5pt}
  /* A fold the reader left closed stays closed on paper: printing every
     fragment would treble the document to say what it already said. */
  details.src:not([open]){display:none}
  /* A dialog cannot be opened on paper, and the whole spec prints in its own
     section anyway: printing sixty fragments would repeat it sixty times. */
  #srcstore,dialog#srcdlg{display:none!important}
  /* A fold cannot be opened on paper: the introduction prints whole. */
  details.more>summary{display:none}
  details.more>p{display:block!important}
  .srcbtn{display:none}
  .head{display:block;margin:11pt 0 4pt}
  .srcpanel{border:.4pt solid var(--rule);page-break-inside:avoid;break-inside:avoid}
  /* On paper a fold cannot be opened, so everything prints. */
  .yaml details.yf>summary::after{content:""}
  .yaml{border:.4pt solid var(--rule);font-size:7pt;line-height:1.3;padding:5pt 6pt}
  .yaml .yn{min-width:2.6em;padding-right:.7em}
  .yaml details.yf>.yc{display:block!important}
  details.src{border:.4pt solid var(--rule);page-break-inside:avoid;break-inside:avoid}
  details.src pre{padding:4pt 6pt}
  details.src code{font-size:7pt}
  a{color:inherit;text-decoration:none}
  footer{page-break-before:avoid;break-before:avoid;border-top:.4pt solid var(--rule);padding:7pt 0 0;font-size:7.5pt}
}
`

// fluxrigLogo is the mark of what generated the page, carried inline so the file
// stays self-contained. The wordmark's fills are hoisted into classes: the SVG
// hardcodes a dark blue that disappears on a dark ground, and only CSS can reach
// it once the markup is inline.
//
// It says who generated the reference, not whose protocol it describes — the
// document itself carries no branding, because whoever documents their own
// protocol should not find another company's mark on their work.
const themeIcon = `<svg viewBox="0 0 24 24" width="17" height="17" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" aria-hidden="true">` +
	`<g class="sun"><circle cx="12" cy="12" r="4.2"/><path d="M12 2.4v2.2M12 19.4v2.2M2.4 12h2.2M19.4 12h2.2M5.2 5.2l1.6 1.6M17.2 17.2l1.6 1.6M18.8 5.2l-1.6 1.6M6.8 17.2l-1.6 1.6"/></g>` +
	`<g class="moon"><path d="M20 13.4A8 8 0 1 1 10.6 4a6.4 6.4 0 0 0 9.4 9.4z"/></g></svg>`

// The only script on the page. It makes the theme an explicit choice rather
// than the system's, and the page must render correctly without it: reading
// storage can throw on its own in a private window or with site data blocked.
const upIcon = `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><path d="M12 19V5M5 12l7-7 7 7"/></svg>`

const menuIcon = `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M4 7h16M4 12h16M4 17h16"/></svg>`

const closeIcon = `<svg viewBox="0 0 24 24" width="18" height="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" aria-hidden="true"><path d="M6 6l12 12M18 6L6 18"/></svg>`

const pageJS = `
(function(){
  var root = document.documentElement;
  try {
    var saved = localStorage.getItem('protodoc-theme');
    if (saved === 'dark' || saved === 'light') root.setAttribute('data-theme', saved);
  } catch (e) {}
  var btn = document.getElementById('theme');
  if (btn) {
    btn.addEventListener('click', function(){
      var stamped = root.getAttribute('data-theme');
      var current = (stamped === 'dark' || stamped === 'light') ? stamped
        : (window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
      var next = current === 'dark' ? 'light' : 'dark';
      root.setAttribute('data-theme', next);
      try { localStorage.setItem('protodoc-theme', next); } catch (e) {}
    });
  }


  // Tabs. A deep link decides which pane opens, so a link to #de-39 from
  // anywhere lands on the element rather than on whichever pane was first.
  var panes = document.querySelectorAll('.pane'), tabs = document.querySelectorAll('.tab');
  // Which section the reader is in. The index said where you could go and never
  // where you are: hovering showed a colour, and the pointer is wherever the
  // hand left it.
  var spyTargets = [], spyCurrent = null;
  function collectSpy(){
    spyTargets = [];
    // The mark belongs to the index being replaced. Dropping the reference
    // without clearing the attribute leaves it set, so every pane change marked
    // one more entry and the panel ended up claiming the reader was in four
    // places at once.
    document.querySelectorAll('nav a[aria-current]').forEach(function(a){
      a.removeAttribute('aria-current');
    });
    document.querySelectorAll('nav a[data-pane]').forEach(function(a){
      if (a.hidden) return;
      var el = document.getElementById(a.getAttribute('href').slice(1));
      if (el) spyTargets.push({el: el, link: a});
    });
    spyCurrent = null;
  }
  // Keeping the marked entry on screen. Sixty data elements do not fit in the
  // panel, so a marker the reader has to scroll to find marks nothing.
  function keepVisible(link){
    var panel = link.closest('nav');
    if (!panel || panel.scrollHeight <= panel.clientHeight + 4) return;
    var top = link.offsetTop, bottom = top + link.offsetHeight;
    if (top < panel.scrollTop) {
      panel.scrollTop = Math.max(0, top - 8);
    } else if (bottom > panel.scrollTop + panel.clientHeight) {
      panel.scrollTop = bottom - panel.clientHeight + 8;
    }
  }
  function markSection(){
    if (!spyTargets.length) return;
    // The line sits just under the tab bar, which is what covers the top of the
    // page: a heading level with the bar has been reached, not passed. Measured
    // rather than assumed, because the bar wraps at narrow widths and this
    // document is also read inside a frame half a viewport tall.
    var bar = document.querySelector('.tabbar');
    var line = (bar ? bar.getBoundingClientRect().bottom : 0) + 30;
    var current = spyTargets[0];
    for (var i = 0; i < spyTargets.length; i++) {
      if (spyTargets[i].el.getBoundingClientRect().top <= line) current = spyTargets[i];
      else break;
    }
    if (current.link === spyCurrent) return;
    if (spyCurrent) spyCurrent.removeAttribute('aria-current');
    current.link.setAttribute('aria-current', 'location');
    spyCurrent = current.link;
    keepVisible(current.link);
  }
  window.addEventListener('scroll', markSection, {passive:true});

  // The index belongs to the pane on screen: entries for the others are not
  // dimmed, they are gone. A panel listing sixty data elements beside an open
  // glossary is a panel that answers a question nobody asked.
  var navItems = document.querySelectorAll('nav [data-pane]');
  function indexFor(id){
    navItems.forEach(function(n){ n.hidden = n.dataset.pane !== id; });
  }
  function showPane(id, scrollTo){
    if (!id) return false;
    var found = false;
    panes.forEach(function(p){
      var on = p.dataset.pane === id;
      p.hidden = !on;
      found = found || on;
    });
    if (!found) return false;
    tabs.forEach(function(t){ t.setAttribute('aria-selected', String(t.dataset.pane === id)); });
    indexFor(id);
    // A pane change replaces the index, so what it follows is replaced with it.
    collectSpy();
    requestAnimationFrame(markSection);
    if (scrollTo) {
      goTo(document.getElementById(scrollTo));
    } else {
      window.scrollTo({top:0});
    }
    return true;
  }
  // A fragment navigation inside a frame scrolls the frame's ancestors too, so a
  // reader following a cross-reference in an embedded copy of this document
  // loses the page around it. Every internal jump scrolls this window instead,
  // which stops here, and clears the sticky bar on the way.
  function goTo(el){
    if (!el) return;
    var bar = document.querySelector('.tabbar');
    var off = bar ? bar.getBoundingClientRect().height : 0;
    var y = el.getBoundingClientRect().top + window.pageYOffset - off - 8;
    window.scrollTo({top: y < 0 ? 0 : y});
  }
  function paneOf(id){
    var el = document.getElementById(id);
    if (!el) return null;
    var p = el.closest('.pane');
    return p ? p.dataset.pane : null;
  }
  tabs.forEach(function(t){
    t.addEventListener('click', function(){ showPane(t.dataset.pane); history.replaceState(null,'','#pane-'+t.dataset.pane); });
  });
  function openFromHash(){
    var h = (location.hash || '').slice(1);
    if (!h) return;
    if (h.indexOf('pane-') === 0) { showPane(h.slice(5)); return; }
    var p = paneOf(h);
    if (p) showPane(p, h);
  }
  window.addEventListener('hashchange', openFromHash);
  var home = document.getElementById('home');
  // A hash that is already set fires no hashchange, so the mark answers a click
  // of its own: it has to work the second time as well as the first.
  if (home) home.addEventListener('click', function(){ showPane(home.getAttribute('href').slice(6)); });
  // Which pane opens is decided by the first tab, never by document order. The
  // two agreed once and then did not, and a mismatch shows the reader an empty
  // pane with the right tab lit — which looks like a document with nothing in it.
  if (tabs.length) {
    showPane(tabs[0].dataset.pane);
  } else {
    panes.forEach(function(p, i){ p.hidden = i !== 0; });
  }
  openFromHash();


  // A fragment opens in one dialog rather than under its element. Escape and
  // the backdrop close it, which a native dialog gives for free.
  var dlg = document.getElementById('srcdlg');
  var dlgTitle = document.getElementById('srcdlgtitle');
  var dlgBody = document.getElementById('srcdlgbody');
  function openSource(id){
    var panel = document.getElementById('src-' + id);
    if (!panel || !dlg) return;
    dlgTitle.textContent = panel.dataset.title || '';
    dlgBody.innerHTML = panel.innerHTML;
    if (dlg.showModal) { dlg.showModal(); } else { dlg.setAttribute('open',''); }
  }
  function closeSource(){
    if (!dlg) return;
    if (dlg.close) { dlg.close(); } else { dlg.removeAttribute('open'); }
    dlgBody.innerHTML = '';
  }
  document.querySelectorAll('.srcbtn').forEach(function(btn){
    btn.addEventListener('click', function(){ openSource(btn.dataset.src); });
  });
  // A word explains itself where it stands. Following the link would land the
  // reader in the glossary having lost the table they were reading, so the click
  // is intercepted — and the link is left in place, so it still works without
  // script and still means something when the page is saved.
  document.addEventListener('click', function(e){
    var a = e.target.closest && e.target.closest('a.term');
    if (!a) return;
    var key = (a.getAttribute('href') || '').replace('#help-', '');
    if (!document.getElementById('src-term-' + key)) return;
    e.preventDefault();
    openSource('term-' + key);
  });
  // Anchors keep their href: the document still works without script, and a
  // saved copy still means something. The jump itself is taken over.
  document.addEventListener('click', function(e){
    var a = e.target.closest && e.target.closest('a[href^="#"]');
    if (!a || a.classList.contains('term')) return;
    var href = a.getAttribute('href') || '';
    var id = href.slice(1);
    if (!id) return;
    e.preventDefault();
    if (id.indexOf('pane-') === 0) {
      showPane(id.slice(5));
    } else {
      var p = paneOf(id);
      if (p) { showPane(p, id); } else { goTo(document.getElementById(id)); }
    }
    history.replaceState(null, '', href);
  });

  // An embedding page names the pane by message. A fragment in a frame's URL
  // makes the browser scroll the embedding page to the frame as it loads, which
  // is how an inline copy of this document steals the reader's place.
  window.addEventListener('message', function(e){
    if (!e.data || typeof e.data.fluxrigPane !== 'string') return;
    showPane(e.data.fluxrigPane);
  });

  var dlgClose = document.getElementById('srcdlgclose');
  if (dlgClose) dlgClose.addEventListener('click', closeSource);
  // Clicking the backdrop is a click on the dialog itself, outside its box.
  if (dlg) dlg.addEventListener('click', function(e){
    if (e.target === dlg) closeSource();
  });
  if (dlg) dlg.addEventListener('close', function(){ dlgBody.innerHTML = ''; });


  // The mark in the bar appears once the header carrying the other one has gone.
  var tabhome = document.querySelector('.tabhome');
  var header = document.querySelector('header');
  function markInBar(){
    if (!tabhome || !header) return;
    tabhome.classList.toggle('on', window.scrollY > header.offsetHeight - 12);
  }
  window.addEventListener('scroll', markInBar, {passive:true});
  markInBar();

  // Back to top appears once there is a way back worth taking.
  var top = document.getElementById('top');
  if (top) {
    var show = function(){ top.classList.toggle('on', window.scrollY > 600); };
    window.addEventListener('scroll', show, {passive:true});
    show();
    top.addEventListener('click', function(){ window.scrollTo({top:0, behavior:'smooth'}); });
  }

  // The index as a panel on a narrow screen, where sixty entries above the
  // content is a wall to scroll past rather than a table of contents.
  var nav = document.querySelector('nav'), menu = document.getElementById('menu'), scrim = document.querySelector('.scrim');
  function setMenu(open){
    if (!nav) return;
    // The class goes on the scrolling element and nothing is repositioned: the
    // page stays where it was, and the panel keeps its own scroll.
    root.classList.toggle('navopen', open);
    nav.classList.toggle('open', open);
    if (scrim) scrim.classList.toggle('on', open);
    if (menu) menu.setAttribute('aria-expanded', String(open));
  }
  // A window widened past the breakpoint turns the panel back into a sidebar,
  // and a body still frozen for a panel that is no longer one cannot scroll.
  window.addEventListener('resize', function(){
    if (window.innerWidth > 880) setMenu(false);
  });
  if (menu) menu.addEventListener('click', function(){ setMenu(!nav.classList.contains('open')); });
  if (scrim) scrim.addEventListener('click', function(){ setMenu(false); });
  var navclose = document.getElementById('navclose');
  if (navclose) navclose.addEventListener('click', function(){ setMenu(false); });
  // Following a link is the point of opening it, so it closes behind you.
  if (nav) nav.addEventListener('click', function(e){ if (e.target.closest('a')) setMenu(false); });
  document.addEventListener('keydown', function(e){ if (e.key === 'Escape') setMenu(false); });
})();
`
