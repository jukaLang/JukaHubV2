/* elfdyn.h — a small, allocation-free ELF64 dynamic-section reader.
 *
 * AndroidRT reads every bionic library twice: once as a *file* (before
 * anything is mapped) to list what it needs — DT_NEEDED, the undefined
 * symbols its relocations reference, their symbol versions — and once more
 * while the loader (bionicld.c) maps and relocates it. Both passes use this
 * reader over a read-only mmap of the file, so a 100 MB libil2cpp.so costs
 * page-cache pages for its .dynsym/.dynstr/.hash only.
 *
 * Everything is bounds-checked against the file: a truncated or hostile .so
 * yields an error string, never a crash. Types are spelled out here (not
 * <elf.h>) so the reader builds on any host the smoke tests run on.
 *
 * Relocation tables understood: DT_RELA/DT_REL, DT_JMPREL, Android's packed
 * "APS2" tables (DT_ANDROID_RELA/REL, `-Wl,--pack-dyn-relocs=android`) and
 * DT_RELR (standard and DT_ANDROID_RELR) relative-relocation bitmaps.
 */
#ifndef ANDROIDRT_ELFDYN_H
#define ANDROIDRT_ELFDYN_H

#include <stddef.h>
#include <stdint.h>

/* ── ELF64 structures (System V gABI) ─────────────────────────────── */

typedef struct {
    unsigned char e_ident[16];
    uint16_t e_type;
    uint16_t e_machine;
    uint32_t e_version;
    uint64_t e_entry;
    uint64_t e_phoff;
    uint64_t e_shoff;
    uint32_t e_flags;
    uint16_t e_ehsize;
    uint16_t e_phentsize;
    uint16_t e_phnum;
    uint16_t e_shentsize;
    uint16_t e_shnum;
    uint16_t e_shstrndx;
} ApakElfEhdr;

typedef struct {
    uint32_t p_type;
    uint32_t p_flags;
    uint64_t p_offset;
    uint64_t p_vaddr;
    uint64_t p_paddr;
    uint64_t p_filesz;
    uint64_t p_memsz;
    uint64_t p_align;
} ApakElfPhdr;

typedef struct {
    uint32_t sh_name;
    uint32_t sh_type;
    uint64_t sh_flags;
    uint64_t sh_addr;
    uint64_t sh_offset;
    uint64_t sh_size;
    uint32_t sh_link;
    uint32_t sh_info;
    uint64_t sh_addralign;
    uint64_t sh_entsize;
} ApakElfShdr;

typedef struct {
    int64_t d_tag;
    uint64_t d_val;
} ApakElfDyn;

typedef struct {
    uint32_t st_name;
    unsigned char st_info;
    unsigned char st_other;
    uint16_t st_shndx;
    uint64_t st_value;
    uint64_t st_size;
} ApakElfSym;

typedef struct {
    uint64_t r_offset;
    uint64_t r_info;
    int64_t r_addend;
} ApakElfRela;

typedef struct {
    uint64_t r_offset;
    uint64_t r_info;
} ApakElfRel;

typedef struct {
    uint16_t vn_version;
    uint16_t vn_cnt;
    uint32_t vn_file;
    uint32_t vn_aux;
    uint32_t vn_next;
} ApakElfVerneed;

typedef struct {
    uint32_t vna_hash;
    uint16_t vna_flags;
    uint16_t vna_other;
    uint32_t vna_name;
    uint32_t vna_next;
} ApakElfVernaux;

typedef struct {
    uint16_t vd_version;
    uint16_t vd_flags;
    uint16_t vd_ndx;
    uint16_t vd_cnt;
    uint32_t vd_hash;
    uint32_t vd_aux;
    uint32_t vd_next;
} ApakElfVerdef;

typedef struct {
    uint32_t vda_name;
    uint32_t vda_next;
} ApakElfVerdaux;

/* ── constants ─────────────────────────────────────────────────────── */

#define APAK_ET_DYN 3

#define APAK_EM_386     3
#define APAK_EM_ARM     40
#define APAK_EM_X86_64  62
#define APAK_EM_AARCH64 183

#define APAK_PT_LOAD         1
#define APAK_PT_DYNAMIC      2
#define APAK_PT_INTERP       3
#define APAK_PT_PHDR         6
#define APAK_PT_TLS          7
#define APAK_PT_GNU_EH_FRAME 0x6474e550u
#define APAK_PT_GNU_STACK    0x6474e551u
#define APAK_PT_GNU_RELRO    0x6474e552u

#define APAK_PF_X 1u
#define APAK_PF_W 2u
#define APAK_PF_R 4u

#define APAK_SHT_DYNSYM 11u

#define APAK_DT_NULL            0
#define APAK_DT_NEEDED          1
#define APAK_DT_PLTRELSZ        2
#define APAK_DT_HASH            4
#define APAK_DT_STRTAB          5
#define APAK_DT_SYMTAB          6
#define APAK_DT_RELA            7
#define APAK_DT_RELASZ          8
#define APAK_DT_RELAENT         9
#define APAK_DT_STRSZ           10
#define APAK_DT_SYMENT          11
#define APAK_DT_INIT            12
#define APAK_DT_FINI            13
#define APAK_DT_SONAME          14
#define APAK_DT_RPATH           15
#define APAK_DT_SYMBOLIC        16
#define APAK_DT_REL             17
#define APAK_DT_RELSZ           18
#define APAK_DT_RELENT          19
#define APAK_DT_PLTREL          20
#define APAK_DT_TEXTREL         22
#define APAK_DT_JMPREL          23
#define APAK_DT_BIND_NOW        24
#define APAK_DT_INIT_ARRAY      25
#define APAK_DT_FINI_ARRAY      26
#define APAK_DT_INIT_ARRAYSZ    27
#define APAK_DT_FINI_ARRAYSZ    28
#define APAK_DT_RUNPATH         29
#define APAK_DT_FLAGS           30
#define APAK_DT_PREINIT_ARRAY   32
#define APAK_DT_PREINIT_ARRAYSZ 33
#define APAK_DT_RELRSZ          35
#define APAK_DT_RELR            36
#define APAK_DT_RELRENT         37
#define APAK_DT_ANDROID_REL     0x6000000fLL
#define APAK_DT_ANDROID_RELSZ   0x60000010LL
#define APAK_DT_ANDROID_RELA    0x60000011LL
#define APAK_DT_ANDROID_RELASZ  0x60000012LL
#define APAK_DT_ANDROID_RELR    0x6fffe000LL
#define APAK_DT_ANDROID_RELRSZ  0x6fffe001LL
#define APAK_DT_ANDROID_RELRENT 0x6fffe003LL
#define APAK_DT_GNU_HASH        0x6ffffef5LL
#define APAK_DT_VERSYM          0x6ffffff0LL
#define APAK_DT_FLAGS_1         0x6ffffffbLL
#define APAK_DT_VERDEF          0x6ffffffcLL
#define APAK_DT_VERDEFNUM       0x6ffffffdLL
#define APAK_DT_VERNEED         0x6ffffffeLL
#define APAK_DT_VERNEEDNUM      0x6fffffffLL

#define APAK_DF_SYMBOLIC 0x2u
#define APAK_DF_TEXTREL  0x4u
#define APAK_DF_BIND_NOW 0x8u

#define APAK_SHN_UNDEF 0

#define APAK_STB_LOCAL      0
#define APAK_STB_GLOBAL     1
#define APAK_STB_WEAK       2
#define APAK_STB_GNU_UNIQUE 10

#define APAK_STT_NOTYPE    0
#define APAK_STT_OBJECT    1
#define APAK_STT_FUNC      2
#define APAK_STT_SECTION   3
#define APAK_STT_FILE      4
#define APAK_STT_COMMON    5
#define APAK_STT_TLS       6
#define APAK_STT_GNU_IFUNC 10

#define APAK_STV_DEFAULT   0
#define APAK_STV_INTERNAL  1
#define APAK_STV_HIDDEN    2
#define APAK_STV_PROTECTED 3

#define APAK_ST_BIND(info) ((unsigned)(info) >> 4)
#define APAK_ST_TYPE(info) ((unsigned)(info) & 0xfu)
#define APAK_ST_VIS(other) ((unsigned)(other) & 0x3u)

#define APAK_R_SYM(info)  ((uint32_t)((uint64_t)(info) >> 32))
#define APAK_R_TYPE(info) ((uint32_t)((uint64_t)(info) & 0xffffffffu))

#define APAK_VERSYM_HIDDEN 0x8000u
#define APAK_VER_FLG_WEAK  0x2u

/* ── the parsed view ───────────────────────────────────────────────── */

typedef struct ApakElf {
    const uint8_t *data;      /* the whole file (read-only mapping)      */
    size_t size;
    const ApakElfEhdr *eh;
    const ApakElfPhdr *ph;
    unsigned phnum;

    const ApakElfDyn *dyn;
    size_t ndyn;
    const char *strtab;
    size_t strsz;
    const ApakElfSym *syms;
    size_t nsyms;
    const uint16_t *versym;   /* NULL when unversioned                   */
    uint64_t verneedVaddr;
    size_t nverneed;
    uint64_t verdefVaddr;
    size_t nverdef;
    const uint32_t *sysvHash;
    const uint32_t *gnuHash;
    size_t gnuHashBytes;      /* bytes readable from gnuHash              */
    size_t sysvHashBytes;
    const char *soname;

    const ApakElfRela *rela;
    size_t nrela;
    const ApakElfRel *rel;
    size_t nrel;
    const uint8_t *jmprel;
    size_t jmprelBytes;
    int jmprelIsRela;
    const uint8_t *aps;       /* Android packed relocations, after "APS2" */
    size_t apsBytes;
    int apsIsRela;
    const uint64_t *relr;
    size_t nrelr;

    uint64_t init;            /* vaddrs; 0 = absent                       */
    uint64_t fini;
    uint64_t initArray;
    size_t initArrayCount;
    uint64_t finiArray;
    size_t finiArrayCount;
    uint64_t preinitArray;
    size_t preinitArrayCount;

    uint64_t flags;
    uint64_t flags1;
    int textrel;
    int symbolic;

    uint64_t loadMin;         /* lowest PT_LOAD vaddr (not page-aligned)  */
    uint64_t loadMax;         /* highest PT_LOAD vaddr + memsz           */
    const ApakElfPhdr *tls;   /* PT_TLS or NULL                           */
    const ApakElfPhdr *ehFrameHdr;
    const ApakElfPhdr *relro;

    char err[200];
} ApakElf;

/* One relocation, whatever table it came from. RELR bitmap entries are
 * reported with relr=1 (an implicit R_*_RELATIVE with addend = the word
 * already at the target, like DT_REL). */
typedef struct {
    uint64_t offset;
    uint64_t info;
    int64_t addend;
    int hasAddend;   /* 1: RELA addend above; 0: implicit (REL/RELR)     */
    int relr;        /* 1: from a DT_RELR bitmap                          */
    int plt;         /* 1: from DT_JMPREL                                 */
} ApakReloc;

/* Return 0 to continue, anything else to stop the walk with that value. */
typedef int (*ApakRelocFn)(void *ctx, const ApakReloc *r);

/* Parse an ELF64 little-endian ET_DYN image. 0 on success, -1 with e->err. */
int apak_elfParse(ApakElf *e, const void *data, size_t size);

/* Bounded views. NULL when the range is not inside the file. */
const void *apak_elfAt(const ApakElf *e, uint64_t vaddr, size_t len);
const char *apak_elfStr(const ApakElf *e, uint64_t offset);

/* Names. */
const char *apak_elfSymName(const ApakElf *e, size_t index);
size_t apak_elfNeededCount(const ApakElf *e);
const char *apak_elfNeeded(const ApakElf *e, size_t index);
const char *apak_elfMachineName(unsigned machine);

/* Symbol version of dynsym[index]: the version name (NULL when none) and,
 * for an import, the library the version was required from (verneed
 * vn_file). `file` and `hidden` are optional outputs. */
const char *apak_elfSymVersion(const ApakElf *e, size_t index, const char **file,
                               int *hidden);

/* A DEFINED, exported (GLOBAL/WEAK/UNIQUE, default/protected visibility,
 * not version-hidden) symbol by name, via DT_GNU_HASH or DT_HASH (linear
 * scan when the file has neither). Returns the dynsym index or -1. */
long apak_elfLookup(const ApakElf *e, const char *name);

/* Every relocation in every table, in bionic's application order (APS2,
 * RELR, RELA, REL, JMPREL). Returns 0, the callback's stop value, or -1 when
 * a table is malformed (e->err says which). */
int apak_elfForEachReloc(ApakElf *e, ApakRelocFn fn, void *ctx);

/* The APS2 decoder on its own (data starts AFTER the 4-byte "APS2" magic),
 * exposed for the unit test. */
int apak_elfDecodeAps2(const uint8_t *data, size_t len, int isRela,
                       ApakRelocFn fn, void *ctx, char *err, size_t errCap);

/* The hash functions, exposed for the unit test. */
uint32_t apak_elfGnuHash(const char *name);
uint32_t apak_elfSysvHash(const char *name);

#endif /* ANDROIDRT_ELFDYN_H */
