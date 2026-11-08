/* aps2_test.c — unit test for elfdyn.c's Android packed-relocation decoder.
 *
 * Real NDK libraries linked with -Wl,--pack-dyn-relocs=android carry their
 * relocations as an "APS2" SLEB128 stream (DT_ANDROID_RELA). No host tool
 * here emits one, so this test checks the decoder two ways:
 *   1. a vector computed by hand from bionic's packed_reloc_iterator rules;
 *   2. round trips through an encoder covering every group-flag
 *      combination (grouped/ungrouped info, offset delta and addend, groups
 *      with and without addends, negative deltas, REL and RELA).
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "elfdyn.h"

static int g_failures;

static void check(int ok, const char *what) {
    fprintf(stderr, "[aps2-test] %s: %s\n", ok ? "ok" : "FAIL", what);
    if (!ok) g_failures++;
}

typedef struct {
    ApakReloc got[64];
    int n;
} Sink;

static int collect(void *ctx, const ApakReloc *r) {
    Sink *s = (Sink *)ctx;
    if (s->n < 64) s->got[s->n] = *r;
    s->n++;
    return 0;
}

/* ── SLEB128 encoder ─────────────────────────────────────────────────── */
static size_t sleb(unsigned char *out, int64_t v) {
    size_t n = 0;
    int more = 1;
    while (more) {
        unsigned char byte = (unsigned char)(v & 0x7f);
        v >>= 7; /* arithmetic shift on every supported compiler */
        if ((v == 0 && !(byte & 0x40)) || (v == -1 && (byte & 0x40))) more = 0;
        else byte |= 0x80;
        out[n++] = byte;
    }
    return n;
}

typedef struct {
    uint64_t offset, info;
    int64_t addend;
} Rel;

/* Encode `rels` as ONE group with the given flags, after `count`/base. */
static size_t encodeOneGroup(unsigned char *buf, const Rel *rels, int n, int flags, int isRela) {
    size_t w = 0;
    int i;
    int64_t prevAddend = 0;
    uint64_t base = rels[0].offset - (n > 1 ? rels[1].offset - rels[0].offset : 8);
    w += sleb(buf + w, n);
    w += sleb(buf + w, (int64_t)base);
    w += sleb(buf + w, n);
    w += sleb(buf + w, flags);
    if (flags & 2) w += sleb(buf + w, (int64_t)(n > 1 ? rels[1].offset - rels[0].offset : 8));
    if (flags & 1) w += sleb(buf + w, (int64_t)rels[0].info);
    if ((flags & 8) && (flags & 4)) {
        w += sleb(buf + w, rels[0].addend);
        prevAddend = rels[0].addend;
    }
    for (i = 0; i < n; i++) {
        uint64_t prevOff = i ? rels[i - 1].offset : base;
        if (!(flags & 2)) w += sleb(buf + w, (int64_t)(rels[i].offset - prevOff));
        if (!(flags & 1)) w += sleb(buf + w, (int64_t)rels[i].info);
        if (isRela && (flags & 8) && !(flags & 4)) {
            w += sleb(buf + w, rels[i].addend - prevAddend);
            prevAddend = rels[i].addend;
        }
    }
    return w;
}

static int sameAs(const Sink *s, const Rel *rels, int n, int isRela, int addends) {
    int i;
    if (s->n != n) return 0;
    for (i = 0; i < n; i++) {
        if (s->got[i].offset != rels[i].offset || s->got[i].info != rels[i].info) return 0;
        if (isRela && s->got[i].addend != (addends ? rels[i].addend : 0)) return 0;
    }
    return 1;
}

int main(void) {
    char err[200];
    Sink s;

    /* 1. by hand: 3 relocs, base 0xff8, one group {size 3, flags
     *    BY_INFO|BY_OFFSET_DELTA|HAS_ADDEND = 0x0b}, delta 8, info 8,
     *    per-reloc addend deltas +0x10 -> addends 0x10, 0x20, 0x30 */
    {
        static const unsigned char v[] = { 0x03, 0xf8, 0x1f, 0x03, 0x0b, 0x08, 0x08, 0x10, 0x10, 0x10 };
        memset(&s, 0, sizeof s);
        check(apak_elfDecodeAps2(v, sizeof v, 1, collect, &s, err, sizeof err) == 0 && s.n == 3 &&
                  s.got[0].offset == 0x1000 && s.got[1].offset == 0x1008 && s.got[2].offset == 0x1010 &&
                  s.got[0].info == 8 && s.got[2].info == 8 && s.got[0].addend == 0x10 && s.got[1].addend == 0x20 &&
                  s.got[2].addend == 0x30,
              "hand-computed APS2 vector decodes to 3 RELATIVE relocations");
        memset(&s, 0, sizeof s);
        check(apak_elfDecodeAps2(v, sizeof v - 1, 1, collect, &s, err, sizeof err) == -1 && strstr(err, "truncated"),
              "a truncated stream is an error, not a crash");
    }

    /* 2. round trips over every flag combination */
    {
        static const Rel relr[] = { { 0x2000, 1027, 0x40 }, { 0x2010, 1027, 0x20 }, { 0x2020, 1027, 0x60 } };
        static const Rel mixed[] = { { 0x3000, (5ull << 32) | 1025, 0 }, { 0x2ff0, (9ull << 32) | 257, -16 },
                                     { 0x3100, (2ull << 32) | 1026, 7 } };
        int flags, isRela, ok = 1;
        for (isRela = 0; isRela <= 1; isRela++) {
            for (flags = 0; flags < 16; flags++) {
                unsigned char buf[256];
                size_t len;
                int rc;
                /* grouped-by-* flags need identical values across the group */
                const Rel *set = (flags & 1) || (flags & 2) ? relr : mixed;
                if (!isRela && (flags & 8)) continue; /* any addend flag is invalid in REL */
                len = encodeOneGroup(buf, set, 3, flags, isRela);
                memset(&s, 0, sizeof s);
                rc = apak_elfDecodeAps2(buf, len, isRela, collect, &s, err, sizeof err);
                if (rc != 0) {
                    fprintf(stderr, "[aps2-test] flags=%d rela=%d: %s\n", flags, isRela, err);
                    ok = 0;
                    continue;
                }
                if ((flags & 4) && !(flags & 8)) {
                    /* BY_ADDEND without HAS_ADDEND: addends reset to 0 */
                    if (!sameAs(&s, set, 3, isRela, 0)) ok = 0;
                } else if ((flags & 8) && (flags & 4)) {
                    /* one addend for the whole group */
                    int i;
                    for (i = 0; i < 3; i++)
                        if (s.got[i].addend != set[0].addend) ok = 0;
                } else if (!sameAs(&s, set, 3, isRela, (flags & 8) != 0)) {
                    fprintf(stderr, "[aps2-test] mismatch flags=%d rela=%d\n", flags, isRela);
                    ok = 0;
                }
            }
        }
        check(ok, "encode/decode round trips for all 16 group-flag combinations, REL and RELA");
    }

    /* 3. any addend flag in a REL table is rejected like bionic does */
    {
        static const unsigned char grouped[] = { 0x01, 0x00, 0x01, 0x0c, 0x05, 0x08, 0x08 };
        static const unsigned char perReloc[] = { 0x01, 0x00, 0x01, 0x08, 0x08, 0x08, 0x05 };
        memset(&s, 0, sizeof s);
        check(apak_elfDecodeAps2(grouped, sizeof grouped, 0, collect, &s, err, sizeof err) == -1 && strstr(err, "REL") &&
                  apak_elfDecodeAps2(perReloc, sizeof perReloc, 0, collect, &s, err, sizeof err) == -1 && s.n == 0,
              "an addend flag in a REL table is an error (grouped or per relocation)");
    }

    /* 4. hashes against known values */
    check(apak_elfGnuHash("") == 5381 && apak_elfGnuHash("printf") == 0x156b2bb8u, "GNU hash");
    check(apak_elfSysvHash("printf") == 0x077905a6u, "SysV ELF hash");

    fprintf(stderr, "[aps2-test] %s\n", g_failures ? "FAILED" : "all checks passed");
    return g_failures ? 1 : 0;
}
