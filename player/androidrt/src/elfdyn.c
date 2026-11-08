/* elfdyn.c — see elfdyn.h. No allocation, no globals, no libc beyond
 * string.h/stdio.h: safe to run on any file before anything is mapped. */
#include "elfdyn.h"

#include <stdio.h>
#include <string.h>

static int elfFail(ApakElf *e, const char *msg) {
    snprintf(e->err, sizeof e->err, "%s", msg);
    return -1;
}

/* The PT_LOAD whose file-backed bytes contain vaddr, and how many bytes are
 * readable from there to the end of that segment's file content. */
static const uint8_t *elfSpan(const ApakElf *e, uint64_t vaddr, size_t *avail) {
    unsigned i;
    *avail = 0;
    for (i = 0; i < e->phnum; i++) {
        const ApakElfPhdr *p = &e->ph[i];
        uint64_t rel, off, room;
        if (p->p_type != APAK_PT_LOAD || vaddr < p->p_vaddr) continue;
        rel = vaddr - p->p_vaddr;
        if (rel >= p->p_filesz) continue;
        off = p->p_offset + rel;
        if (off >= e->size) return NULL;
        room = p->p_filesz - rel;
        if (room > e->size - off) room = e->size - off;
        *avail = (size_t)room;
        return e->data + off;
    }
    return NULL;
}

const void *apak_elfAt(const ApakElf *e, uint64_t vaddr, size_t len) {
    size_t avail = 0;
    const uint8_t *p = elfSpan(e, vaddr, &avail);
    if (!p || len > avail) return NULL;
    return p;
}

const char *apak_elfStr(const ApakElf *e, uint64_t offset) {
    if (!e->strtab || offset >= e->strsz) return NULL;
    if (!memchr(e->strtab + offset, '\0', e->strsz - (size_t)offset)) return NULL;
    return e->strtab + offset;
}

const char *apak_elfSymName(const ApakElf *e, size_t index) {
    if (!e->syms || index >= e->nsyms) return NULL;
    return apak_elfStr(e, e->syms[index].st_name);
}

const char *apak_elfMachineName(unsigned machine) {
    switch (machine) {
        case APAK_EM_AARCH64: return "aarch64 (arm64-v8a)";
        case APAK_EM_ARM:     return "32-bit ARM (armeabi-v7a)";
        case APAK_EM_X86_64:  return "x86_64";
        case APAK_EM_386:     return "x86";
        default:              return "unknown machine";
    }
}

uint32_t apak_elfGnuHash(const char *name) {
    uint32_t h = 5381;
    const unsigned char *p = (const unsigned char *)name;
    while (*p) h = (h << 5) + h + *p++;
    return h;
}

uint32_t apak_elfSysvHash(const char *name) {
    uint32_t h = 0, g;
    const unsigned char *p = (const unsigned char *)name;
    while (*p) {
        h = (h << 4) + *p++;
        g = h & 0xf0000000u;
        if (g) h ^= g >> 24;
        h &= ~g;
    }
    return h;
}

/* Symbol count from DT_GNU_HASH: the highest bucket start, walked to the end
 * of its chain. 0 when the table is malformed. */
static size_t gnuHashSymCount(const uint32_t *gh, size_t bytes) {
    uint32_t nbuckets, symoffset, bloomSize, last = 0, b;
    size_t hdr, chainWords;
    const uint32_t *buckets, *chain;
    if (!gh || bytes < 16) return 0;
    nbuckets = gh[0];
    symoffset = gh[1];
    bloomSize = gh[2];
    hdr = 16 + (size_t)bloomSize * 8;
    if (bytes < hdr || (bytes - hdr) / 4 < nbuckets) return 0;
    buckets = (const uint32_t *)((const uint8_t *)gh + hdr);
    chain = buckets + nbuckets;
    chainWords = (bytes - hdr) / 4 - nbuckets;
    for (b = 0; b < nbuckets; b++)
        if (buckets[b] > last) last = buckets[b];
    if (last < symoffset) return symoffset;
    for (;;) {
        size_t ci = (size_t)(last - symoffset);
        if (ci >= chainWords) return 0;
        if (chain[ci] & 1u) return (size_t)last + 1;
        last++;
    }
}

static size_t dynsymCountFromSections(const ApakElf *e, uint64_t symtabVaddr) {
    const ApakElfEhdr *eh = e->eh;
    size_t i;
    if (!eh->e_shoff || eh->e_shentsize != sizeof(ApakElfShdr) || !eh->e_shnum) return 0;
    if (eh->e_shoff > e->size || (e->size - eh->e_shoff) / sizeof(ApakElfShdr) < eh->e_shnum) return 0;
    for (i = 0; i < eh->e_shnum; i++) {
        const ApakElfShdr *sh = (const ApakElfShdr *)(e->data + eh->e_shoff) + i;
        if (sh->sh_type != APAK_SHT_DYNSYM) continue;
        if (sh->sh_entsize != sizeof(ApakElfSym)) return 0;
        if (sh->sh_addr && sh->sh_addr != symtabVaddr) return 0;
        return (size_t)(sh->sh_size / sizeof(ApakElfSym));
    }
    return 0;
}

int apak_elfParse(ApakElf *e, const void *data, size_t size) {
    const ApakElfEhdr *eh;
    const ApakElfPhdr *dynPh = NULL;
    unsigned i;
    size_t d, avail;
    uint64_t strtabV = 0, symtabV = 0, hashV = 0, gnuHashV = 0, versymV = 0;
    uint64_t relaV = 0, relaSz = 0, relV = 0, relSz = 0, jmprelV = 0, pltrelSz = 0, pltrel = 0;
    uint64_t aRelV = 0, aRelSz = 0, aRelaV = 0, aRelaSz = 0, relrV = 0, relrSz = 0;
    uint64_t sonameOff = 0, initArraySz = 0, finiArraySz = 0, preinitArraySz = 0;
    int haveSoname = 0;

    memset(e, 0, sizeof *e);
    e->data = (const uint8_t *)data;
    e->size = size;
    if (!data || size < sizeof(ApakElfEhdr)) return elfFail(e, "file too small to be an ELF library");
    eh = (const ApakElfEhdr *)data;
    if (memcmp(eh->e_ident, "\177ELF", 4) != 0) return elfFail(e, "not an ELF file");
    if (eh->e_ident[4] == 1)
        return elfFail(e, "32-bit ELF (ELFCLASS32): this runner loads 64-bit (arm64-v8a) libraries only");
    if (eh->e_ident[4] != 2) return elfFail(e, "unknown ELF class");
    if (eh->e_ident[5] != 1) return elfFail(e, "big-endian ELF");
    if (eh->e_type != APAK_ET_DYN) return elfFail(e, "not a shared library (e_type is not ET_DYN)");
    if (eh->e_phentsize != sizeof(ApakElfPhdr) || eh->e_phnum == 0)
        return elfFail(e, "bad program header table");
    if (eh->e_phoff > size || (size - eh->e_phoff) / sizeof(ApakElfPhdr) < eh->e_phnum)
        return elfFail(e, "program headers run past the end of the file");
    e->eh = eh;
    e->ph = (const ApakElfPhdr *)(e->data + eh->e_phoff);
    e->phnum = eh->e_phnum;

    e->loadMin = UINT64_MAX;
    for (i = 0; i < e->phnum; i++) {
        const ApakElfPhdr *p = &e->ph[i];
        switch (p->p_type) {
            case APAK_PT_LOAD:
                if (p->p_filesz > p->p_memsz) return elfFail(e, "PT_LOAD with filesz > memsz");
                if (p->p_filesz && (p->p_offset > size || p->p_filesz > size - p->p_offset))
                    return elfFail(e, "PT_LOAD runs past the end of the file (truncated library?)");
                if (p->p_vaddr < e->loadMin) e->loadMin = p->p_vaddr;
                if (p->p_vaddr + p->p_memsz > e->loadMax) e->loadMax = p->p_vaddr + p->p_memsz;
                break;
            case APAK_PT_DYNAMIC: dynPh = p; break;
            case APAK_PT_TLS: e->tls = p; break;
            case APAK_PT_GNU_EH_FRAME: e->ehFrameHdr = p; break;
            case APAK_PT_GNU_RELRO: e->relro = p; break;
            default: break;
        }
    }
    if (e->loadMin == UINT64_MAX) return elfFail(e, "no PT_LOAD segments");
    if (!dynPh) return elfFail(e, "no PT_DYNAMIC (a static binary, not a shared library)");
    if (dynPh->p_offset > size || dynPh->p_filesz > size - dynPh->p_offset)
        return elfFail(e, "PT_DYNAMIC runs past the end of the file");

    e->dyn = (const ApakElfDyn *)(e->data + dynPh->p_offset);
    e->ndyn = (size_t)(dynPh->p_filesz / sizeof(ApakElfDyn));
    for (d = 0; d < e->ndyn; d++) {
        const ApakElfDyn *x = &e->dyn[d];
        if (x->d_tag == APAK_DT_NULL) {
            e->ndyn = d;
            break;
        }
        switch (x->d_tag) {
            case APAK_DT_STRTAB: strtabV = x->d_val; break;
            case APAK_DT_STRSZ: e->strsz = (size_t)x->d_val; break;
            case APAK_DT_SYMTAB: symtabV = x->d_val; break;
            case APAK_DT_SYMENT:
                if (x->d_val != sizeof(ApakElfSym)) return elfFail(e, "unexpected DT_SYMENT");
                break;
            case APAK_DT_HASH: hashV = x->d_val; break;
            case APAK_DT_GNU_HASH: gnuHashV = x->d_val; break;
            case APAK_DT_VERSYM: versymV = x->d_val; break;
            case APAK_DT_VERNEED: e->verneedVaddr = x->d_val; break;
            case APAK_DT_VERNEEDNUM: e->nverneed = (size_t)x->d_val; break;
            case APAK_DT_VERDEF: e->verdefVaddr = x->d_val; break;
            case APAK_DT_VERDEFNUM: e->nverdef = (size_t)x->d_val; break;
            case APAK_DT_RELA: relaV = x->d_val; break;
            case APAK_DT_RELASZ: relaSz = x->d_val; break;
            case APAK_DT_RELAENT:
                if (x->d_val != sizeof(ApakElfRela)) return elfFail(e, "unexpected DT_RELAENT");
                break;
            case APAK_DT_REL: relV = x->d_val; break;
            case APAK_DT_RELSZ: relSz = x->d_val; break;
            case APAK_DT_RELENT:
                if (x->d_val != sizeof(ApakElfRel)) return elfFail(e, "unexpected DT_RELENT");
                break;
            case APAK_DT_JMPREL: jmprelV = x->d_val; break;
            case APAK_DT_PLTRELSZ: pltrelSz = x->d_val; break;
            case APAK_DT_PLTREL: pltrel = x->d_val; break;
            case APAK_DT_ANDROID_REL: aRelV = x->d_val; break;
            case APAK_DT_ANDROID_RELSZ: aRelSz = x->d_val; break;
            case APAK_DT_ANDROID_RELA: aRelaV = x->d_val; break;
            case APAK_DT_ANDROID_RELASZ: aRelaSz = x->d_val; break;
            case APAK_DT_RELR:
            case APAK_DT_ANDROID_RELR: relrV = x->d_val; break;
            case APAK_DT_RELRSZ:
            case APAK_DT_ANDROID_RELRSZ: relrSz = x->d_val; break;
            case APAK_DT_RELRENT:
            case APAK_DT_ANDROID_RELRENT:
                if (x->d_val != 8) return elfFail(e, "unexpected DT_RELRENT");
                break;
            case APAK_DT_SONAME: sonameOff = x->d_val; haveSoname = 1; break;
            case APAK_DT_INIT: e->init = x->d_val; break;
            case APAK_DT_FINI: e->fini = x->d_val; break;
            case APAK_DT_INIT_ARRAY: e->initArray = x->d_val; break;
            case APAK_DT_INIT_ARRAYSZ: initArraySz = x->d_val; break;
            case APAK_DT_FINI_ARRAY: e->finiArray = x->d_val; break;
            case APAK_DT_FINI_ARRAYSZ: finiArraySz = x->d_val; break;
            case APAK_DT_PREINIT_ARRAY: e->preinitArray = x->d_val; break;
            case APAK_DT_PREINIT_ARRAYSZ: preinitArraySz = x->d_val; break;
            case APAK_DT_FLAGS: e->flags = x->d_val; break;
            case APAK_DT_FLAGS_1: e->flags1 = x->d_val; break;
            case APAK_DT_TEXTREL: e->textrel = 1; break;
            case APAK_DT_SYMBOLIC: e->symbolic = 1; break;
            default: break;
        }
    }
    if (e->flags & APAK_DF_TEXTREL) e->textrel = 1;
    if (e->flags & APAK_DF_SYMBOLIC) e->symbolic = 1;

    if (!strtabV || !e->strsz) return elfFail(e, "no dynamic string table");
    e->strtab = (const char *)apak_elfAt(e, strtabV, e->strsz);
    if (!e->strtab) return elfFail(e, "dynamic string table is outside the file");
    if (!symtabV) return elfFail(e, "no dynamic symbol table");

    if (gnuHashV) {
        e->gnuHash = (const uint32_t *)elfSpan(e, gnuHashV, &avail);
        e->gnuHashBytes = e->gnuHash ? avail : 0;
    }
    if (hashV) {
        e->sysvHash = (const uint32_t *)elfSpan(e, hashV, &avail);
        e->sysvHashBytes = e->sysvHash ? avail : 0;
        if (e->sysvHash && (avail < 8 || (avail - 8) / 4 < (size_t)e->sysvHash[0] + e->sysvHash[1])) {
            e->sysvHash = NULL;
            e->sysvHashBytes = 0;
        }
    }
    e->nsyms = dynsymCountFromSections(e, symtabV);
    if (!e->nsyms && e->sysvHash) e->nsyms = e->sysvHash[1];
    if (!e->nsyms && e->gnuHash) e->nsyms = gnuHashSymCount(e->gnuHash, e->gnuHashBytes);
    if (!e->nsyms) return elfFail(e, "cannot size the dynamic symbol table (no hash table, no sections)");
    e->syms = (const ApakElfSym *)apak_elfAt(e, symtabV, e->nsyms * sizeof(ApakElfSym));
    if (!e->syms) return elfFail(e, "dynamic symbol table is outside the file");
    if (versymV) {
        e->versym = (const uint16_t *)apak_elfAt(e, versymV, e->nsyms * sizeof(uint16_t));
        if (!e->versym) return elfFail(e, "symbol version table is outside the file");
    }

    /* glibc's rule: a DT_RELASZ that also covers .rela.plt is trimmed so the
     * PLT relocations are not applied twice. */
    if (jmprelV && pltrelSz) {
        if (pltrel == APAK_DT_RELA && relaV && relaV + relaSz == jmprelV + pltrelSz && relaSz >= pltrelSz)
            relaSz -= pltrelSz;
        if (pltrel == APAK_DT_REL && relV && relV + relSz == jmprelV + pltrelSz && relSz >= pltrelSz)
            relSz -= pltrelSz;
    }
    if (relaV && relaSz) {
        e->rela = (const ApakElfRela *)apak_elfAt(e, relaV, (size_t)relaSz);
        if (!e->rela) return elfFail(e, "DT_RELA table is outside the file");
        e->nrela = (size_t)(relaSz / sizeof(ApakElfRela));
    }
    if (relV && relSz) {
        e->rel = (const ApakElfRel *)apak_elfAt(e, relV, (size_t)relSz);
        if (!e->rel) return elfFail(e, "DT_REL table is outside the file");
        e->nrel = (size_t)(relSz / sizeof(ApakElfRel));
    }
    if (jmprelV && pltrelSz) {
        if (pltrel != APAK_DT_RELA && pltrel != APAK_DT_REL) return elfFail(e, "unknown DT_PLTREL");
        e->jmprel = (const uint8_t *)apak_elfAt(e, jmprelV, (size_t)pltrelSz);
        if (!e->jmprel) return elfFail(e, "DT_JMPREL table is outside the file");
        e->jmprelBytes = (size_t)pltrelSz;
        e->jmprelIsRela = pltrel == APAK_DT_RELA;
    }
    if ((aRelaV && aRelaSz) || (aRelV && aRelSz)) {
        int isRela = aRelaV && aRelaSz;
        uint64_t v = isRela ? aRelaV : aRelV, sz = isRela ? aRelaSz : aRelSz;
        const uint8_t *p = (const uint8_t *)apak_elfAt(e, v, (size_t)sz);
        if (!p || sz < 4) return elfFail(e, "Android packed relocation table is outside the file");
        if (memcmp(p, "APS2", 4) != 0) return elfFail(e, "Android packed relocations without the APS2 magic");
        e->aps = p + 4;
        e->apsBytes = (size_t)sz - 4;
        e->apsIsRela = isRela;
    }
    if (relrV && relrSz) {
        e->relr = (const uint64_t *)apak_elfAt(e, relrV, (size_t)relrSz);
        if (!e->relr) return elfFail(e, "DT_RELR table is outside the file");
        e->nrelr = (size_t)(relrSz / 8);
    }
    if (haveSoname) e->soname = apak_elfStr(e, sonameOff);
    e->initArrayCount = (size_t)(initArraySz / 8);
    e->finiArrayCount = (size_t)(finiArraySz / 8);
    e->preinitArrayCount = (size_t)(preinitArraySz / 8);
    return 0;
}

size_t apak_elfNeededCount(const ApakElf *e) {
    size_t d, n = 0;
    for (d = 0; d < e->ndyn; d++)
        if (e->dyn[d].d_tag == APAK_DT_NEEDED) n++;
    return n;
}

const char *apak_elfNeeded(const ApakElf *e, size_t index) {
    size_t d, n = 0;
    for (d = 0; d < e->ndyn; d++) {
        if (e->dyn[d].d_tag != APAK_DT_NEEDED) continue;
        if (n++ == index) return apak_elfStr(e, e->dyn[d].d_val);
    }
    return NULL;
}

const char *apak_elfSymVersion(const ApakElf *e, size_t index, const char **file, int *hidden) {
    uint16_t v;
    int undef, pass;
    if (file) *file = NULL;
    if (hidden) *hidden = 0;
    if (!e->versym || index >= e->nsyms) return NULL;
    v = e->versym[index];
    if (hidden) *hidden = (v & APAK_VERSYM_HIDDEN) != 0;
    v &= 0x7fff;
    if (v < 2) return NULL; /* 0 = local, 1 = the unversioned base */
    undef = e->syms[index].st_shndx == APAK_SHN_UNDEF;
    /* Imports name their version in verneed, definitions in verdef; look in
     * the expected table first and fall back to the other. */
    for (pass = 0; pass < 2; pass++) {
        int needTable = (pass == 0) ? undef : !undef;
        if (needTable && e->verneedVaddr) {
            uint64_t at = e->verneedVaddr;
            size_t n;
            for (n = 0; n < e->nverneed && n < 4096; n++) {
                const ApakElfVerneed *vn = (const ApakElfVerneed *)apak_elfAt(e, at, sizeof *vn);
                uint64_t auxAt;
                unsigned a;
                if (!vn) break;
                auxAt = at + vn->vn_aux;
                for (a = 0; a < vn->vn_cnt && a < 4096; a++) {
                    const ApakElfVernaux *vna = (const ApakElfVernaux *)apak_elfAt(e, auxAt, sizeof *vna);
                    if (!vna) break;
                    if ((vna->vna_other & 0x7fff) == v) {
                        if (file) *file = apak_elfStr(e, vn->vn_file);
                        return apak_elfStr(e, vna->vna_name);
                    }
                    if (!vna->vna_next) break;
                    auxAt += vna->vna_next;
                }
                if (!vn->vn_next) break;
                at += vn->vn_next;
            }
        }
        if (!needTable && e->verdefVaddr) {
            uint64_t at = e->verdefVaddr;
            size_t n;
            for (n = 0; n < e->nverdef && n < 4096; n++) {
                const ApakElfVerdef *vd = (const ApakElfVerdef *)apak_elfAt(e, at, sizeof *vd);
                if (!vd) break;
                if ((vd->vd_ndx & 0x7fff) == v) {
                    const ApakElfVerdaux *vda = (const ApakElfVerdaux *)apak_elfAt(e, at + vd->vd_aux, sizeof *vda);
                    return vda ? apak_elfStr(e, vda->vda_name) : NULL;
                }
                if (!vd->vd_next) break;
                at += vd->vd_next;
            }
        }
    }
    return NULL;
}

static int isExport(const ApakElf *e, size_t index) {
    const ApakElfSym *s = &e->syms[index];
    unsigned bind = APAK_ST_BIND(s->st_info), type = APAK_ST_TYPE(s->st_info);
    unsigned vis = APAK_ST_VIS(s->st_other);
    if (s->st_shndx == APAK_SHN_UNDEF) return 0;
    if (bind != APAK_STB_GLOBAL && bind != APAK_STB_WEAK && bind != APAK_STB_GNU_UNIQUE) return 0;
    if (vis != APAK_STV_DEFAULT && vis != APAK_STV_PROTECTED) return 0;
    if (type == APAK_STT_SECTION || type == APAK_STT_FILE) return 0;
    if (e->versym && (e->versym[index] & APAK_VERSYM_HIDDEN)) return 0;
    return 1;
}

static int nameIs(const ApakElf *e, size_t index, const char *name) {
    const char *n = apak_elfSymName(e, index);
    return n && strcmp(n, name) == 0;
}

long apak_elfLookup(const ApakElf *e, const char *name) {
    if (!name || !*name) return -1;
    if (e->gnuHash && e->gnuHashBytes >= 16) {
        const uint32_t *gh = e->gnuHash;
        uint32_t nbuckets = gh[0], symoffset = gh[1], bloomSize = gh[2], bloomShift = gh[3];
        size_t hdr = 16 + (size_t)bloomSize * 8, chainWords;
        uint32_t h = apak_elfGnuHash(name), idx;
        const uint64_t *bloom = (const uint64_t *)(gh + 4);
        const uint32_t *buckets, *chain;
        uint64_t word, mask;
        if (!nbuckets || !bloomSize || e->gnuHashBytes < hdr || (e->gnuHashBytes - hdr) / 4 < nbuckets)
            return -1;
        buckets = (const uint32_t *)((const uint8_t *)gh + hdr);
        chain = buckets + nbuckets;
        chainWords = (e->gnuHashBytes - hdr) / 4 - nbuckets;
        word = bloom[(h / 64) % bloomSize];
        mask = (1ull << (h % 64)) | (1ull << ((h >> bloomShift) % 64));
        if ((word & mask) != mask) return -1;
        idx = buckets[h % nbuckets];
        if (idx == 0 || idx < symoffset) return -1;
        for (;; idx++) {
            size_t ci = (size_t)(idx - symoffset);
            uint32_t h2;
            if (ci >= chainWords || idx >= e->nsyms) return -1;
            h2 = chain[ci];
            if ((h | 1u) == (h2 | 1u) && nameIs(e, idx, name) && isExport(e, idx)) return (long)idx;
            if (h2 & 1u) return -1;
        }
    }
    if (e->sysvHash) {
        const uint32_t *hh = e->sysvHash;
        uint32_t nbucket = hh[0], nchain = hh[1], i, guard = 0;
        const uint32_t *bucket = hh + 2, *chain = bucket + nbucket;
        if (!nbucket) return -1;
        for (i = bucket[apak_elfSysvHash(name) % nbucket]; i != 0 && i < nchain && i < e->nsyms; i = chain[i]) {
            if (nameIs(e, i, name) && isExport(e, i)) return (long)i;
            if (++guard > nchain) break;
        }
        return -1;
    }
    {
        size_t i;
        for (i = 1; i < e->nsyms; i++)
            if (isExport(e, i) && nameIs(e, i, name)) return (long)i;
    }
    return -1;
}

/* ── relocations ───────────────────────────────────────────────────── */

typedef struct {
    const uint8_t *p;
    const uint8_t *end;
    int bad;
} Sleb;

static int64_t slebNext(Sleb *s) {
    uint64_t value = 0;
    unsigned shift = 0;
    uint8_t byte;
    do {
        if (s->p >= s->end) {
            s->bad = 1;
            return 0;
        }
        byte = *s->p++;
        if (shift < 64) value |= (uint64_t)(byte & 0x7f) << shift;
        shift += 7;
    } while (byte & 0x80);
    if (shift < 64 && (byte & 0x40)) value |= ~(uint64_t)0 << shift;
    return (int64_t)value;
}

int apak_elfDecodeAps2(const uint8_t *data, size_t len, int isRela, ApakRelocFn fn, void *ctx,
                       char *err, size_t errCap) {
    Sleb s;
    uint64_t count, done = 0;
    ApakReloc r;
    s.p = data;
    s.end = data + len;
    s.bad = 0;
    memset(&r, 0, sizeof r);
    r.hasAddend = isRela;
    count = (uint64_t)slebNext(&s);
    r.offset = (uint64_t)slebNext(&s);
    if (s.bad) goto truncated;
    while (done < count) {
        uint64_t groupSize = (uint64_t)slebNext(&s), groupFlags = (uint64_t)slebNext(&s), i;
        uint64_t groupDelta = 0;
        int byInfo = (groupFlags & 1) != 0, byOffset = (groupFlags & 2) != 0;
        int byAddend = (groupFlags & 4) != 0, hasAddend = (groupFlags & 8) != 0;
        if (s.bad) goto truncated;
        if (groupSize == 0) {
            if (err) snprintf(err, errCap, "APS2: empty relocation group");
            return -1;
        }
        if (byOffset) groupDelta = (uint64_t)slebNext(&s);
        if (byInfo) r.info = (uint64_t)slebNext(&s);
        if (hasAddend && !isRela) {
            /* bionic's REL platforms reject any addend flag here */
            if (err) snprintf(err, errCap, "APS2: addend flag in a REL (non-RELA) table");
            return -1;
        }
        if (hasAddend && byAddend) r.addend += slebNext(&s);
        else if (!hasAddend && isRela) r.addend = 0;
        if (s.bad) goto truncated;
        for (i = 0; i < groupSize && done < count; i++) {
            int rc;
            if (byOffset) r.offset += groupDelta;
            else r.offset += (uint64_t)slebNext(&s);
            if (!byInfo) r.info = (uint64_t)slebNext(&s);
            if (isRela && hasAddend && !byAddend) r.addend += slebNext(&s);
            if (s.bad) goto truncated;
            done++;
            rc = fn(ctx, &r);
            if (rc) return rc;
        }
    }
    return 0;
truncated:
    if (err) snprintf(err, errCap, "APS2: truncated relocation stream");
    return -1;
}

int apak_elfForEachReloc(ApakElf *e, ApakRelocFn fn, void *ctx) {
    ApakReloc r;
    size_t i;
    int rc;

    if (e->aps) {
        rc = apak_elfDecodeAps2(e->aps, e->apsBytes, e->apsIsRela, fn, ctx, e->err, sizeof e->err);
        if (rc) return rc;
    }
    if (e->relr) {
        uint64_t where = 0;
        memset(&r, 0, sizeof r);
        r.relr = 1;
        for (i = 0; i < e->nrelr; i++) {
            uint64_t entry = e->relr[i];
            if ((entry & 1) == 0) {
                r.offset = entry;
                if ((rc = fn(ctx, &r)) != 0) return rc;
                where = entry + 8;
            } else {
                uint64_t bits = entry >> 1, k;
                for (k = 0; bits; k++, bits >>= 1) {
                    if (!(bits & 1)) continue;
                    r.offset = where + k * 8;
                    if ((rc = fn(ctx, &r)) != 0) return rc;
                }
                where += 63 * 8;
            }
        }
    }
    for (i = 0; i < e->nrela; i++) {
        memset(&r, 0, sizeof r);
        r.offset = e->rela[i].r_offset;
        r.info = e->rela[i].r_info;
        r.addend = e->rela[i].r_addend;
        r.hasAddend = 1;
        if ((rc = fn(ctx, &r)) != 0) return rc;
    }
    for (i = 0; i < e->nrel; i++) {
        memset(&r, 0, sizeof r);
        r.offset = e->rel[i].r_offset;
        r.info = e->rel[i].r_info;
        if ((rc = fn(ctx, &r)) != 0) return rc;
    }
    if (e->jmprel) {
        size_t ent = e->jmprelIsRela ? sizeof(ApakElfRela) : sizeof(ApakElfRel);
        size_t n = e->jmprelBytes / ent;
        for (i = 0; i < n; i++) {
            memset(&r, 0, sizeof r);
            r.plt = 1;
            if (e->jmprelIsRela) {
                const ApakElfRela *x = (const ApakElfRela *)e->jmprel + i;
                r.offset = x->r_offset;
                r.info = x->r_info;
                r.addend = x->r_addend;
                r.hasAddend = 1;
            } else {
                const ApakElfRel *x = (const ApakElfRel *)e->jmprel + i;
                r.offset = x->r_offset;
                r.info = x->r_info;
            }
            if ((rc = fn(ctx, &r)) != 0) return rc;
        }
    }
    return 0;
}
