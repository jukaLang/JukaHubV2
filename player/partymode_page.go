package main

// partymode_page.go — the page a phone opens from Party Mode's QR code.
//
// One self-contained document: no fonts, scripts or pictures from the
// internet, so it works on a hotspot with no uplink. Names and titles are
// only ever set with textContent. The token from the join lives in
// sessionStorage for this room code, so reloading the page keeps the seat;
// the last name typed is kept in localStorage for the next party.
//
// Keep this file free of backquotes: the page is a Go raw string.

const pmodePageHTML = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover">
<meta name="theme-color" content="#160b33">
<title>JukaHub Party</title>
<style>
:root{color-scheme:dark;--bg:#160b33;--card:rgba(255,255,255,.07);--line:rgba(255,255,255,.12);--ink:#f5f3ff;--muted:#b9b2d9;--accent:#ffcf4d;--hot:#ff4d6d}
*{box-sizing:border-box;-webkit-tap-highlight-color:transparent}
html,body{margin:0;min-height:100%;background:radial-gradient(120% 80% at 50% -10%,#4b1f8f 0%,#22104a 45%,#120828 100%) fixed;color:var(--ink);font:16px/1.4 system-ui,-apple-system,"Segoe UI",Roboto,sans-serif}
#app{max-width:520px;margin:0 auto;padding:calc(10px + env(safe-area-inset-top)) 16px calc(16px + env(safe-area-inset-bottom));min-height:100vh;min-height:100dvh;display:flex;flex-direction:column}
.hidden{display:none!important}
.top{display:flex;align-items:center;justify-content:space-between;gap:8px;margin-bottom:10px}
.brand{font-weight:600;letter-spacing:.3px;color:var(--muted)}
.brand b{color:var(--accent)}
.chip{display:flex;align-items:center;gap:8px;background:var(--card);border:1px solid var(--line);border-radius:999px;padding:5px 12px;font-weight:600;max-width:65%}
.chip .nm{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.dot{width:12px;height:12px;border-radius:50%;flex:none;background:#888}
.view{flex:1;display:flex;flex-direction:column;gap:14px}
.hero{text-align:center;margin:18px 0 6px}
.logo{width:92px;height:92px;margin:0 auto 12px;border-radius:26px;display:grid;place-items:center;font-size:54px;font-weight:800;color:#1b0d3a;background:linear-gradient(135deg,#ffe36e,#ff8a5b 55%,#ff4d8d);box-shadow:0 10px 40px rgba(255,120,90,.45);transform:rotate(-6deg)}
h1{font-size:30px;margin:0}
h2{font-size:24px;margin:6px 0 0;text-align:center}
p{margin:0}
.muted{color:var(--muted);text-align:center}
label{display:flex;flex-direction:column;gap:6px;font-weight:600;color:var(--muted)}
input{font:inherit;font-size:20px;color:var(--ink);background:rgba(0,0,0,.25);border:2px solid var(--line);border-radius:14px;padding:14px 16px;outline:none}
input:focus{border-color:var(--accent)}
button{font:inherit;font-weight:700;color:var(--ink);background:var(--card);border:1px solid var(--line);border-radius:16px;padding:14px 18px;cursor:pointer;touch-action:manipulation}
button:disabled{opacity:.45;cursor:default}
.big{font-size:20px;padding:18px}
.primary{background:linear-gradient(135deg,#ffcf4d,#ff7a59);color:#1b0d3a;border:0;box-shadow:0 8px 24px rgba(255,140,80,.35)}
.ghost{background:transparent;color:var(--muted);border:0}
.err{color:#ff9aa9;text-align:center;min-height:1.4em;font-weight:600}
.plist{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:8px}
.plist li{display:flex;align-items:center;gap:10px;background:var(--card);border:1px solid var(--line);border-radius:14px;padding:10px 14px;font-weight:600}
.plist li.you{border-color:var(--accent)}
.plist li.away{opacity:.45}
.plist .nm{flex:1;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}
.plist .sc{font-variant-numeric:tabular-nums}
.plist .rk{width:26px;color:var(--muted);font-variant-numeric:tabular-nums}
.plist .dl{font-size:14px;min-width:48px;text-align:right;font-variant-numeric:tabular-nums}
.up{color:#7dffa1}.down{color:#ff8fa0}
.roundbar{display:flex;justify-content:space-between;align-items:baseline;font-weight:700}
.pts{color:var(--accent);font-size:20px}
.stage{position:relative}
.frame{position:relative;width:100%;aspect-ratio:1/1;border-radius:22px;overflow:hidden;background:#0c0620;border:2px solid var(--line);box-shadow:0 14px 40px rgba(0,0,0,.45);display:grid;place-items:center}
.frame img{width:100%;height:100%;object-fit:cover;image-rendering:pixelated;opacity:0;transform:scale(1.04);transition:opacity .25s,transform .35s}
.frame img.on{opacity:1;transform:none}
.frame img.full{object-fit:contain;image-rendering:auto;background:#0c0620}
.wait{position:absolute;top:0;right:0;bottom:0;left:0;display:grid;place-items:center;text-align:center;font-size:26px;font-weight:800;padding:20px;background:radial-gradient(circle,#3b1a78,#120828)}
.dots{display:flex;justify-content:center;gap:8px;margin-top:10px}
.dots i{width:10px;height:10px;border-radius:50%;background:var(--line)}
.dots i.on{background:var(--accent)}
.timer{height:8px;border-radius:8px;background:var(--line);overflow:hidden}
.timer div{height:100%;width:100%;background:linear-gradient(90deg,#ffcf4d,#ff4d6d);transform-origin:left;will-change:transform}
.status{text-align:center;font-size:19px;font-weight:700;min-height:1.5em}
.buzz{align-self:center;width:min(62vw,230px);height:min(62vw,230px);border-radius:50%;font-size:34px;letter-spacing:2px;color:#fff;border:0;background:radial-gradient(circle at 35% 30%,#ff8098,#ff3159 55%,#c0123a);box-shadow:0 12px 0 #8a0b29,0 18px 40px rgba(255,49,89,.55);transition:transform .06s,box-shadow .06s}
.buzz:active:not(:disabled){transform:translateY(8px);box-shadow:0 4px 0 #8a0b29,0 8px 20px rgba(255,49,89,.5)}
.buzz:disabled{background:radial-gradient(circle at 35% 30%,#8a84a8,#5d5780 60%,#3d3860);box-shadow:0 12px 0 #2a2645;opacity:.7}
.buzz.pulse{animation:pulse 1.4s ease-in-out infinite}
@keyframes pulse{0%,100%{box-shadow:0 12px 0 #8a0b29,0 18px 40px rgba(255,49,89,.45)}50%{box-shadow:0 12px 0 #8a0b29,0 18px 70px rgba(255,49,89,.9)}}
.choices{display:grid;grid-template-columns:1fr 1fr;gap:10px}
.choices button{min-height:92px;border:0;border-radius:18px;font-size:17px;line-height:1.25;color:#fff;display:flex;flex-direction:column;align-items:flex-start;justify-content:space-between;text-align:left;word-break:break-word}
.choices button b{font-size:13px;opacity:.85;background:rgba(0,0,0,.25);border-radius:8px;padding:2px 8px}
.c0{background:#e2344f}.c1{background:#2f6fe4}.c2{background:#9a6400}.c3{background:#17804a}
.result{text-align:center;background:var(--card);border:1px solid var(--line);border-radius:18px;padding:14px}
.restitle{font-size:24px;font-weight:800}
.reswho{margin-top:8px;font-size:18px;font-weight:700;color:var(--accent)}
.board{margin-top:16px}
.board .plist li{padding:8px 12px}
.trophy{font-size:84px;text-align:center;margin-top:10px;animation:bob 1.6s ease-in-out infinite}
@keyframes bob{0%,100%{transform:translateY(0)}50%{transform:translateY(-10px)}}
.ranks li:nth-child(1){background:linear-gradient(90deg,rgba(255,207,77,.35),var(--card))}
.ranks li:nth-child(2){background:linear-gradient(90deg,rgba(200,210,230,.25),var(--card))}
.ranks li:nth-child(3){background:linear-gradient(90deg,rgba(205,127,50,.3),var(--card))}
.flash{animation:flash .5s ease-out}
@keyframes flash{from{filter:brightness(2.2)}to{filter:none}}
@supports not (aspect-ratio:1/1){.frame{display:block;height:0;padding-bottom:100%}.frame img{position:absolute;top:0;left:0}}
@media (prefers-reduced-motion:reduce){.buzz.pulse,.trophy,.flash{animation:none}.frame img{transition:none}}
</style>
</head>
<body>
<div id="app">
<header class="top">
 <div class="brand">JukaHub <b>Party</b></div>
 <div id="meChip" class="chip hidden"><span id="meDot" class="dot"></span><span id="meName" class="nm"></span><span id="meScore">0</span></div>
</header>

<section id="vJoin" class="view">
 <div class="hero"><div class="logo" aria-hidden="true">?</div><h1>Guess the Game</h1><p class="muted">Join the game show on the handheld</p></div>
 <label>Your name<input id="name" maxlength="16" autocomplete="nickname" autocapitalize="words" enterkeyhint="go" spellcheck="false"></label>
 <label id="codeRow" class="hidden">Party code<input id="code" maxlength="8" autocapitalize="characters" autocomplete="off" spellcheck="false"></label>
 <button id="joinBtn" class="big primary">Join the party</button>
 <p id="joinErr" class="err" role="alert"></p>
</section>

<section id="vLobby" class="view hidden">
 <h2>You're in!</h2>
 <p class="muted" id="lobbyNote">Waiting for the host to start</p>
 <ul id="lobbyList" class="plist"></ul>
 <p class="muted">Watch the handheld's screen, or play right here: the cover shows on your phone too.</p>
 <button id="leaveBtn" class="ghost">Leave the party</button>
</section>

<section id="vGame" class="view hidden">
 <div class="roundbar"><span id="roundLbl"></span><span id="ptsLbl" class="pts"></span></div>
 <div class="stage">
  <div class="frame" id="frame"><img id="pic" alt=""><div id="picWait" class="wait"></div></div>
  <div id="dots" class="dots"></div>
 </div>
 <div class="timer"><div id="timerFill"></div></div>
 <p id="status" class="status" aria-live="polite"></p>
 <button id="buzz" class="buzz">BUZZ</button>
 <div id="choices" class="choices hidden"></div>
 <div id="result" class="result hidden"><div id="resTitle" class="restitle"></div><div id="resSys" class="muted"></div><div id="resWho" class="reswho"></div><div id="resMiss" class="muted"></div></div>
</section>

<section id="vFinal" class="view hidden">
 <div class="trophy" aria-hidden="true">&#127942;</div>
 <h2 id="finalHead"></h2>
 <ol id="podium" class="plist ranks"></ol>
 <p class="muted">The host can start another game from the handheld.</p>
</section>

<section id="vGone" class="view hidden">
 <div class="hero"><div class="logo" aria-hidden="true">!</div></div>
 <h2>Lost the handheld</h2>
 <p class="muted" id="goneMsg">Party Mode closed, or this phone left the Wi-Fi.</p>
 <button id="retryBtn" class="big">Try again</button>
</section>

<footer id="board" class="board hidden"><ul id="boardList" class="plist"></ul></footer>
</div>
<script>
(function(){
'use strict';
var COLORS=['#ff5d73','#ffb84d','#ffe45e','#5ee07a','#4dd4ff','#6e8bff','#c77dff','#ff7ad9'];
var q=new URLSearchParams(location.search);
var room=(q.get('room')||'').toUpperCase().replace(/\s+/g,'');
var token=null,ver=0,st=null,gotAt=0,polling=false,pollCtl=null,fails=0,wantImg='',audio=null,barW=-1;
function $(id){return document.getElementById(id);}
function key(){return 'pm-token-'+room;}
try{token=room?sessionStorage.getItem(key()):null;}catch(e){}
try{$('name').value=localStorage.getItem('pm-name')||'';}catch(e){}
if(!room){$('codeRow').classList.remove('hidden');}

function show(id){['vJoin','vLobby','vGame','vFinal','vGone'].forEach(function(v){$(v).classList.toggle('hidden',v!==id);});}
function color(i){return COLORS[((i|0)%COLORS.length+COLORS.length)%COLORS.length];}
function signed(n){return n>0?'+'+n:String(n);}

// ── Sound ──
function unlockAudio(){try{if(!audio){var C=window.AudioContext||window.webkitAudioContext;if(C){audio=new C();}}if(audio&&audio.state!=='running'){audio.resume();}}catch(e){}}
// Browsers only start sound from a tap: any tap will do, including after a reload.
['pointerdown','touchend','keydown'].forEach(function(t){document.addEventListener(t,unlockAudio,true);});
function tone(f,dur,type,at,vol){if(!audio){return;}try{var t=audio.currentTime+(at||0),o=audio.createOscillator(),g=audio.createGain();o.type=type||'sine';o.frequency.setValueAtTime(f,t);g.gain.setValueAtTime(0.0001,t);g.gain.exponentialRampToValueAtTime(vol||0.18,t+0.012);g.gain.exponentialRampToValueAtTime(0.0001,t+dur);o.connect(g);g.connect(audio.destination);o.start(t);o.stop(t+dur+0.05);}catch(e){}}
function seq(fs,step,type){fs.forEach(function(f,i){tone(f,step*1.6,type||'triangle',i*step,0.2);});}
var SFX={
 buzz:function(){tone(196,0.32,'sawtooth',0,0.22);tone(147,0.32,'square',0,0.08);},
 right:function(){seq([523,659,784,1047],0.09);},
 wrong:function(){tone(233,0.22,'square',0,0.13);tone(175,0.4,'square',0.18,0.13);},
 stage:function(){tone(988,0.07,'sine',0,0.1);},
 reveal:function(){seq([392,330,262],0.12,'sine');},
 win:function(){seq([523,659,784,659,784,1047,1319],0.11);}
};
function buzzVibe(p){try{if(navigator.vibrate){navigator.vibrate(p);}}catch(e){}}

// ── Network ──
function post(path,body){
 return fetch(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body),cache:'no-store'}).then(function(res){
  if(res.status===204){return {};}
  return res.json().catch(function(){return {};}).then(function(j){if(!res.ok){var m=j.error||('Error '+res.status);var e=new Error(m.charAt(0).toUpperCase()+m.slice(1));e.status=res.status;throw e;}return j;});
 });
}
function forget(){token=null;st=null;ver=0;try{sessionStorage.removeItem(key());}catch(e){}}
// One long poll at a time, tied to the token it was sent with: an answer for
// a seat this page has since left (or replaced) is dropped, never applied.
// The server holds a poll for 15 s; one with no answer in 25 s is given up,
// so a handheld that went to sleep cannot freeze the page.
function poll(){
 if(polling||!token){return;}
 polling=true;
 var my=token,ctl=window.AbortController?new AbortController():null;
 var timer=setTimeout(function(){if(ctl){ctl.abort();}},25000);
 pollCtl=ctl;
 fetch('/api/pm/state?t='+encodeURIComponent(my)+'&v='+ver,{cache:'no-store',signal:ctl?ctl.signal:undefined}).then(function(res){
  if(my!==token){var x=new Error('stale');x.stale=true;throw x;}
  if(res.status===401){forget();var e=new Error('gone');e.fatal=true;throw e;}
  if(!res.ok){throw new Error('HTTP '+res.status);}
  return res.json();
 }).then(function(s){
  clearTimeout(timer);polling=false;pollCtl=null;
  if(my!==token){poll();return;}
  fails=0;apply(s);setTimeout(poll,0);
 }).catch(function(e){
  clearTimeout(timer);polling=false;pollCtl=null;
  if(e&&e.fatal){showJoin('That party has ended. Scan the code on the handheld to join the new one.');return;}
  if((e&&e.stale)||my!==token){poll();return;}
  if(ctl&&ctl.wake){setTimeout(poll,0);return;}
  fails++;
  if(fails>=4){show('vGone');$('board').classList.add('hidden');}
  setTimeout(poll,Math.min(5000,500*fails));
 });
}
// Back from the lock screen: ask again at once rather than wait out an old poll.
document.addEventListener('visibilitychange',function(){
 if(document.hidden){return;}
 if(polling&&pollCtl){pollCtl.wake=true;pollCtl.abort();}else{poll();}
});

// ── Join ──
function showJoin(msg){show('vJoin');$('meChip').classList.add('hidden');$('board').classList.add('hidden');$('joinErr').textContent=msg||'';if(!room){$('codeRow').classList.remove('hidden');}}
function join(){
 if($('joinBtn').disabled){return;}
 var name=$('name').value.trim();
 if(!name){$('joinErr').textContent='Type your name first.';$('name').focus();return;}
 if(!room||!$('codeRow').classList.contains('hidden')){
  var c=$('code').value.trim().toUpperCase().replace(/\s+/g,'');
  if(c){room=c;}
 }
 if(!room){$('joinErr').textContent='Type the party code shown on the handheld.';$('codeRow').classList.remove('hidden');return;}
 try{localStorage.setItem('pm-name',name);}catch(e){}
 unlockAudio();
 $('joinBtn').disabled=true;$('joinErr').textContent='';
 post('/api/pm/join',{room:room,name:name}).then(function(r){
  token=r.token;ver=0;st=null;
  try{sessionStorage.setItem(key(),token);}catch(e){}
  // A reload keeps this seat even when the code was typed rather than scanned.
  try{history.replaceState(null,'','/?room='+encodeURIComponent(room));}catch(e){}
  buzzVibe(30);poll();
 }).catch(function(e){$('joinErr').textContent=e.message||'Could not join.';if(e.status===403){$('codeRow').classList.remove('hidden');}}).then(function(){$('joinBtn').disabled=false;});
}
$('joinBtn').addEventListener('click',join);
$('name').addEventListener('keydown',function(e){if(e.key==='Enter'){join();}});
$('code').addEventListener('keydown',function(e){if(e.key==='Enter'){join();}});
$('leaveBtn').addEventListener('click',function(){var t=token;forget();if(t){post('/api/pm/leave',{t:t}).catch(function(){});}showJoin('');});
$('retryBtn').addEventListener('click',function(){fails=0;if(token){poll();}else{location.reload();}});

// ── Rendering ──
function li(p,opts){
 var el=document.createElement('li');
 if(p.you){el.className='you';}
 if(p.away){el.className+=' away';}
 if(opts.rank){var rk=document.createElement('span');rk.className='rk';rk.textContent=p.rank+'.';el.appendChild(rk);}
 var d=document.createElement('span');d.className='dot';d.style.background=color(p.color);el.appendChild(d);
 var n=document.createElement('span');n.className='nm';n.textContent=p.name+(p.you?' (you)':'')+(p.here?' · handheld':'')+(p.out?' ✕':'');el.appendChild(n);
 if(opts.delta&&p.delta){var dl=document.createElement('span');dl.className='dl '+(p.delta>0?'up':'down');dl.textContent=signed(p.delta);el.appendChild(dl);}
 if(opts.score){var sc=document.createElement('span');sc.className='sc';sc.textContent=p.score;el.appendChild(sc);}
 return el;
}
function fill(ul,players,opts){
 var list=players.slice();
 if(opts.rank){list.sort(function(a,b){return a.rank-b.rank;});}
 ul.textContent='';
 list.forEach(function(p){ul.appendChild(li(p,opts));});
}
function setImage(s){
 var pic=$('pic'),wait=$('picWait');
 if(!s.img){pic.classList.remove('on');wantImg='';wait.classList.remove('hidden');return;}
 var url='/api/pm/img?t='+encodeURIComponent(token)+'&g='+s.img_g+'&r='+s.img_r+'&s='+s.img_s;
 var full=s.img_s>=s.stages-1;
 if(url===wantImg){return;}
 wantImg=url;
 var im=new Image();
 im.alt='';
 im.onload=function(){
  if(wantImg!==url){return;}
  // Put the loaded picture itself in the frame: no second download, and the
  // last round's answer never shows while the next one loads.
  var old=$('pic');
  im.id='pic';im.className='on'+(full?' full':'');
  old.parentNode.replaceChild(im,old);
  wait.classList.add('hidden');
  var fr=$('frame');fr.classList.remove('flash');void fr.offsetWidth;fr.classList.add('flash');
 };
 im.onerror=function(){if(wantImg===url){wantImg='';}};
 im.src=url;
}
function dots(s){
 var box=$('dots');
 if(box.children.length!==s.stages){box.textContent='';for(var i=0;i<s.stages;i++){box.appendChild(document.createElement('i'));}}
 var lit=(s.phase==='reveal'||s.phase==='answer')?s.stage:(s.phase==='result'?s.stages-1:-1);
 for(var j=0;j<box.children.length;j++){box.children[j].classList.toggle('on',j<=lit);}
}
function choices(s){
 var box=$('choices');
 if(!s.me.answering){box.classList.add('hidden');box.dataset.key='';return;}
 var k=s.round+':'+s.me.choices.join('|');
 box.classList.remove('hidden');
 if(box.dataset.key===k){return;}
 box.dataset.key=k;box.textContent='';
 s.me.choices.forEach(function(t,i){
  var b=document.createElement('button');b.className='c'+(i%4);
  var tag=document.createElement('b');tag.textContent='ABCD'.charAt(i);
  var tx=document.createElement('span');tx.textContent=t;
  b.appendChild(tag);b.appendChild(tx);
  b.addEventListener('click',function(){
   Array.prototype.forEach.call(box.children,function(x){x.disabled=true;});
   post('/api/pm/answer',{t:token,choice:i}).catch(function(e){
    $('status').textContent=e.message;
    // No answer from the handheld at all: let the pick be tried again.
    if(!e.status){Array.prototype.forEach.call(box.children,function(x){x.disabled=false;});}
   });
  });
  box.appendChild(b);
 });
}
function status(s){
 var me=s.me,t='';
 switch(s.phase){
 case 'intro':t=s.waiting?'Shuffling the next cover…':'Round '+s.round+' — get ready!';break;
 case 'reveal':t=me.locked?'Wrong guess — you are out this round':'Know it? Buzz!';break;
 case 'answer':t=me.answering?'Which game is it?':(s.buzzer+' is answering…');break;
 case 'result':
  if(s.result&&s.result.winner){t=(s.result.winner===me.name?'You got it!':s.result.winner+' got it!')+' +'+s.result.points;}
  else{t='Nobody got it';}
  break;
 }
 $('status').textContent=t;
}
function renderGame(s){
 $('roundLbl').textContent=s.rounds?('Round '+s.round+' of '+s.rounds):'';
 $('ptsLbl').textContent=(s.phase==='reveal'||s.phase==='answer')?(s.points+' pts'):'';
 $('picWait').textContent=s.phase==='intro'?('Round '+s.round):'';
 setImage(s);dots(s);choices(s);status(s);
 var b=$('buzz');
 var buzzable=s.phase==='reveal'&&!s.me.locked;
 b.classList.toggle('hidden',!(s.phase==='reveal'||s.phase==='answer'||s.phase==='intro')||s.me.answering);
 b.disabled=!buzzable;b.classList.toggle('pulse',buzzable);
 var res=$('result');
 if(s.phase==='result'&&s.result){
  res.classList.remove('hidden');
  $('resTitle').textContent=s.result.title;$('resSys').textContent=s.result.system;
  $('resWho').textContent=s.result.winner?(s.result.winner+' +'+s.result.points):'The answer';
  $('resMiss').textContent=s.result.wrong&&s.result.wrong.length?('Missed: '+s.result.wrong.join(', ')):'';
 }else{res.classList.add('hidden');}
}
function topPlayers(s){return s.players.filter(function(p){return p.rank===1;});}
function renderFinal(s){
 var me=s.me,tops=topPlayers(s),best=tops.length?tops[0].score:0;
 var head='Game over';
 if(best>0){
  if(tops.length>1){head=me.rank===1?'You tied for first!':'It\'s a tie!';}
  else{head=me.rank===1?'You win!':tops[0].name+' wins!';}
 }
 $('finalHead').textContent=head;
 fill($('podium'),s.players,{rank:true,score:true});
}
function apply(s){
 var prev=st;st=s;ver=s.v;gotAt=Date.now();
 var me=s.me;
 $('meChip').classList.remove('hidden');
 $('meDot').style.background=color(me.color);$('meName').textContent=me.name;$('meScore').textContent=me.score;
 var board=$('board');
 if(s.phase==='lobby'){
  show('vLobby');board.classList.add('hidden');
  $('lobbyNote').textContent='Waiting for the host to start · '+s.target+' rounds';
  fill($('lobbyList'),s.players,{});
 }else if(s.phase==='final'){
  show('vFinal');board.classList.add('hidden');renderFinal(s);
 }else{
  show('vGame');board.classList.remove('hidden');renderGame(s);
  fill($('boardList'),s.players,{rank:true,score:true,delta:s.phase==='result'});
 }
 sounds(prev,s);
}
function sounds(prev,s){
 if(!prev||!audio){return;}
 var me=s.me;
 if(s.phase==='answer'&&prev.phase!=='answer'){SFX.buzz();if(me.answering){buzzVibe([60,40,60]);}}
 if(s.phase==='reveal'&&prev.phase==='reveal'&&s.stage>prev.stage){SFX.stage();}
 if(me.locked&&!prev.me.locked){SFX.wrong();buzzVibe(220);}
 if(s.phase==='result'&&prev.phase!=='result'){
  if(s.result&&s.result.winner){if(s.result.winner===me.name){SFX.right();buzzVibe([40,30,40,30,90]);}else{SFX.reveal();}}else{SFX.reveal();}
 }
 if(s.phase==='final'&&prev.phase!=='final'){
  var tops=topPlayers(s);
  if(me.rank===1&&tops.length===1&&tops[0].score>0){SFX.win();buzzVibe([80,40,80,40,200]);}else{SFX.reveal();}
 }
}
$('buzz').addEventListener('click',function(){
 unlockAudio();
 if(!token||!st||st.phase!=='reveal'){return;}
 $('buzz').disabled=true;buzzVibe(40);
 post('/api/pm/buzz',{t:token}).catch(function(e){$('status').textContent=e.message;});
});
function frame(){
 var w=0;
 if(st&&st.for_ms>0&&st.phase!=='lobby'&&st.phase!=='final'){
  var left=Math.max(0,st.left_ms-(Date.now()-gotAt));
  w=Math.round(Math.max(0,Math.min(1,left/st.for_ms))*1000)/1000;
 }
 if(w!==barW){barW=w;$('timerFill').style.transform='scaleX('+w+')';}
 requestAnimationFrame(frame);
}
requestAnimationFrame(frame);

if(token){poll();}else{showJoin('');}
})();
</script>
</body>
</html>
`
