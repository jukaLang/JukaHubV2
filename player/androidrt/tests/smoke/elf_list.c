/* elf_list.c — prints what src/elfdyn.c reads from an ELF64 library in the
 * same notation as `readelf --dyn-syms -W` (Type Bind Ndx Name, with
 * name@VER for imports/hidden versions and name@@VER for default ones), so
 * run-smoke.sh can diff the parser against binutils on real host libraries
 * (libc.so.6, libz.so.1, ... — GNU hash, symbol versions, thousands of
 * symbols) and on the NDK-shaped fake game.
 *
 *   elf_list <lib>          symbol listing (one line per dynsym entry)
 *   elf_list -s <lib>       summary: NEEDED/SONAME/relocation count and a
 *                           hash-lookup self check over every export
 */
#define _GNU_SOURCE
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/stat.h>
#include <unistd.h>

#include "elfdyn.h"

static const char *typeName(unsigned t) {
    switch (t) {
        case APAK_STT_NOTYPE: return "NOTYPE";
        case APAK_STT_OBJECT: return "OBJECT";
        case APAK_STT_FUNC: return "FUNC";
        case APAK_STT_SECTION: return "SECTION";
        case APAK_STT_FILE: return "FILE";
        case APAK_STT_COMMON: return "COMMON";
        case APAK_STT_TLS: return "TLS";
        case APAK_STT_GNU_IFUNC: return "IFUNC";
        default: return "?";
    }
}

static const char *bindName(unsigned b) {
    switch (b) {
        case APAK_STB_LOCAL: return "LOCAL";
        case APAK_STB_GLOBAL: return "GLOBAL";
        case APAK_STB_WEAK: return "WEAK";
        case APAK_STB_GNU_UNIQUE: return "UNIQUE";
        default: return "?";
    }
}

static int countReloc(void *ctx, const ApakReloc *r) {
    (void)r;
    ++*(size_t *)ctx;
    return 0;
}

int main(int argc, char **argv) {
    int summary = argc == 3 && strcmp(argv[1], "-s") == 0;
    const char *path = argv[argc - 1];
    struct stat st;
    void *map;
    int fd;
    ApakElf e;
    size_t i;

    if (argc < 2) {
        fprintf(stderr, "usage: elf_list [-s] <lib.so>\n");
        return 64;
    }
    fd = open(path, O_RDONLY | O_CLOEXEC);
    if (fd < 0 || fstat(fd, &st) != 0) {
        perror(path);
        return 1;
    }
    map = mmap(NULL, (size_t)st.st_size, PROT_READ, MAP_PRIVATE, fd, 0);
    close(fd);
    if (map == MAP_FAILED) {
        perror("mmap");
        return 1;
    }
    if (apak_elfParse(&e, map, (size_t)st.st_size) != 0) {
        fprintf(stderr, "parse failed: %s\n", e.err);
        return 1;
    }

    if (!summary) {
        for (i = 1; i < e.nsyms; i++) {
            const ApakElfSym *s = &e.syms[i];
            const char *name = apak_elfSymName(&e, i);
            int hidden = 0;
            const char *ver = apak_elfSymVersion(&e, i, NULL, &hidden);
            const char *sep = "";
            /* readelf prints a version's own base symbol (name == version)
             * without a suffix. */
            if (ver && name && s->st_shndx != APAK_SHN_UNDEF && strcmp(ver, name) == 0) ver = NULL;
            if (ver) sep = (s->st_shndx == APAK_SHN_UNDEF || hidden) ? "@" : "@@";
            printf("%s %s %s %s%s%s\n", typeName(APAK_ST_TYPE(s->st_info)),
                   bindName(APAK_ST_BIND(s->st_info)),
                   s->st_shndx == APAK_SHN_UNDEF ? "UND" : "DEF",
                   name ? name : "(bad-name)", sep, ver ? ver : "");
        }
        return 0;
    }

    {
        size_t relocs = 0, exports = 0, lookupFail = 0;
        printf("SONAME %s\n", e.soname ? e.soname : "(none)");
        for (i = 0; i < apak_elfNeededCount(&e); i++) printf("NEEDED %s\n", apak_elfNeeded(&e, i));
        if (apak_elfForEachReloc(&e, countReloc, &relocs) != 0) {
            printf("RELOCS error: %s\n", e.err);
            return 1;
        }
        printf("RELOCS %zu\n", relocs);
        for (i = 1; i < e.nsyms; i++) {
            const ApakElfSym *s = &e.syms[i];
            const char *name = apak_elfSymName(&e, i);
            unsigned bind = APAK_ST_BIND(s->st_info), vis = APAK_ST_VIS(s->st_other);
            long found;
            if (s->st_shndx == APAK_SHN_UNDEF || !name || !*name) continue;
            if (bind == APAK_STB_LOCAL || (vis != APAK_STV_DEFAULT && vis != APAK_STV_PROTECTED)) continue;
            if (e.versym && (e.versym[i] & APAK_VERSYM_HIDDEN)) continue;
            exports++;
            found = apak_elfLookup(&e, name);
            if (found < 0 || strcmp(apak_elfSymName(&e, (size_t)found), name) != 0) {
                lookupFail++;
                if (lookupFail < 5) printf("LOOKUP-MISS %s\n", name);
            }
        }
        if (apak_elfLookup(&e, "apak_definitely_not_a_symbol_name") >= 0) lookupFail++;
        printf("LOOKUP exports=%zu fail=%zu\n", exports, lookupFail);
        return lookupFail ? 1 : 0;
    }
}
