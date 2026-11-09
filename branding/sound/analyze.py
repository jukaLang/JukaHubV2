import sys, wave, numpy as np
import matplotlib
matplotlib.use('Agg')
import matplotlib.pyplot as plt

def load(p):
    w = wave.open(p)
    sr = w.getframerate(); n = w.getnframes()
    x = np.frombuffer(w.readframes(n), dtype=np.int16).reshape(-1, 2).astype(np.float64) / 32768
    return sr, x

def db(v): return 20*np.log10(max(v, 1e-12))

for p in sys.argv[1:]:
    sr, x = load(p)
    L, R = x[:,0], x[:,1]
    m = (L+R)/2
    peak = np.abs(x).max()
    rms = np.sqrt((x**2).mean())
    # loudness-ish: RMS over the loudest 400ms window
    win = int(0.4*sr)
    e = np.convolve(m**2, np.ones(win)/win, mode='valid')
    st = np.sqrt(e.max())
    # clicks: largest sample-to-sample jump relative to local level
    d = np.abs(np.diff(m))
    # spectrum distribution
    S = np.abs(np.fft.rfft(m * np.hanning(len(m))))**2
    f = np.fft.rfftfreq(len(m), 1/sr)
    tot = S.sum()
    bands = [(0,150),(150,300),(300,600),(600,1200),(1200,2500),(2500,5000),(5000,10000),(10000,24000)]
    dist = ' '.join(f'{a}-{b}:{100*S[(f>=a)&(f<b)].sum()/tot:4.1f}%' for a,b in bands)
    cent = (f*S).sum()/tot
    corr = np.corrcoef(L, R)[0,1]
    mono_loss = db(np.sqrt(((L+R)/2)**2).mean()**0.5) if False else 0
    tail = np.abs(m[-int(0.05*sr):]).max()
    print(f'{p}: {len(m)/sr:.2f}s peak {db(peak):.1f} dBFS, rms {db(rms):.1f}, loudest-400ms {db(st):.1f} dBFS, LR corr {corr:.2f}, '
          f'centroid {cent:.0f} Hz, max step {d.max():.4f}, last-50ms peak {db(tail):.1f} dB, DC {m.mean():.5f}')
    print('   bands:', dist)
    # spectrogram
    fig, ax = plt.subplots(2, 1, figsize=(11, 6.5), gridspec_kw={'height_ratios':[3,1]})
    ax[0].specgram(m, NFFT=2048, Fs=sr, noverlap=1536, cmap='magma', vmin=-120, vmax=-20)
    ax[0].set_ylim(0, 9000); ax[0].set_title(p); ax[0].set_ylabel('Hz')
    t = np.arange(len(m))/sr
    ax[1].plot(t, L, lw=0.3, alpha=0.7, label='L'); ax[1].plot(t, R, lw=0.3, alpha=0.7, label='R')
    ax[1].set_xlim(0, t[-1]); ax[1].set_ylim(-1,1); ax[1].legend(loc='upper right')
    fig.tight_layout(); fig.savefig(p.replace('.wav', '.png'), dpi=80); plt.close(fig)
