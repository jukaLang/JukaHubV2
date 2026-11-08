/* bionicjmp.S — setjmp/longjmp for bionic-linked callers.
 *
 * glibc exports no `sigsetjmp` at all (its header turns it into a macro
 * around __sigsetjmp), so a bionic import of it has nothing to bind to; and
 * the setjmp family must agree with itself, because bionic code may set the
 * jump with one name and take it with another. One implementation serves all
 * six names, sized for bionic's jmp_buf (32 longs on arm64, 11 on x86_64) and
 * independent of glibc's private layout and pointer mangling. It saves into
 * the caller's buffer only what the ABI needs:
 *
 *   arm64  x19..x30, sp, d8..d15, savemask flag, kernel signal mask = 184 B
 *   x86_64 rbx rbp r12..r15 rsp rip, savemask flag, signal mask      =  80 B
 *
 * setjmp saves the signal mask (BSD/bionic semantics), _setjmp does not,
 * sigsetjmp(env, n) does when n != 0; every longjmp variant restores the
 * mask iff it was saved. The mask goes through rt_sigprocmask directly (an
 * 8-byte kernel sigset), so no C library call happens between the save and
 * the caller's frame.
 */

#if defined(__aarch64__)

    .text
    .p2align 2
    .globl apak_setjmp
    .hidden apak_setjmp
    .type apak_setjmp, %function
apak_setjmp:
    mov     w1, #1
    b       apak_sigsetjmp
    .size apak_setjmp, . - apak_setjmp

    .p2align 2
    .globl apak__setjmp
    .hidden apak__setjmp
    .type apak__setjmp, %function
apak__setjmp:
    mov     w1, #0
    b       apak_sigsetjmp
    .size apak__setjmp, . - apak__setjmp

    /* int apak_sigsetjmp(long *env, int saveMask) */
    .p2align 2
    .globl apak_sigsetjmp
    .hidden apak_sigsetjmp
    .type apak_sigsetjmp, %function
apak_sigsetjmp:
    stp     x19, x20, [x0, #0]
    stp     x21, x22, [x0, #16]
    stp     x23, x24, [x0, #32]
    stp     x25, x26, [x0, #48]
    stp     x27, x28, [x0, #64]
    stp     x29, x30, [x0, #80]
    mov     x2, sp
    str     x2, [x0, #96]
    stp     d8, d9, [x0, #104]
    stp     d10, d11, [x0, #120]
    stp     d12, d13, [x0, #136]
    stp     d14, d15, [x0, #152]
    sxtw    x1, w1
    str     x1, [x0, #168]
    cbz     x1, 1f
    /* rt_sigprocmask(SIG_BLOCK, NULL, &env[22], 8) */
    mov     x9, x0
    mov     x0, #0
    mov     x1, #0
    add     x2, x9, #176
    mov     x3, #8
    mov     x8, #135
    svc     #0
1:
    mov     w0, #0
    ret
    .size apak_sigsetjmp, . - apak_sigsetjmp

    /* void apak_siglongjmp(long *env, int value) — also longjmp/_longjmp */
    .p2align 2
    .globl apak_siglongjmp
    .hidden apak_siglongjmp
    .type apak_siglongjmp, %function
    .globl apak_longjmp
    .hidden apak_longjmp
    .type apak_longjmp, %function
    .globl apak__longjmp
    .hidden apak__longjmp
    .type apak__longjmp, %function
apak_siglongjmp:
apak_longjmp:
apak__longjmp:
    mov     x9, x0
    mov     w10, w1
    ldr     x1, [x9, #168]
    cbz     x1, 2f
    /* rt_sigprocmask(SIG_SETMASK, &env[22], NULL, 8) */
    mov     x0, #2
    add     x1, x9, #176
    mov     x2, #0
    mov     x3, #8
    mov     x8, #135
    svc     #0
2:
    ldp     x19, x20, [x9, #0]
    ldp     x21, x22, [x9, #16]
    ldp     x23, x24, [x9, #32]
    ldp     x25, x26, [x9, #48]
    ldp     x27, x28, [x9, #64]
    ldp     x29, x30, [x9, #80]
    ldr     x2, [x9, #96]
    mov     sp, x2
    ldp     d8, d9, [x9, #104]
    ldp     d10, d11, [x9, #120]
    ldp     d12, d13, [x9, #136]
    ldp     d14, d15, [x9, #152]
    cmp     w10, #0
    csinc   w0, w10, wzr, ne
    ret
    .size apak_siglongjmp, . - apak_siglongjmp

    .section .note.GNU-stack, "", %progbits

#elif defined(__x86_64__)

    .text
    .p2align 4
    .globl apak_setjmp
    .hidden apak_setjmp
    .type apak_setjmp, @function
apak_setjmp:
    movl    $1, %esi
    jmp     apak_sigsetjmp
    .size apak_setjmp, . - apak_setjmp

    .p2align 4
    .globl apak__setjmp
    .hidden apak__setjmp
    .type apak__setjmp, @function
apak__setjmp:
    xorl    %esi, %esi
    jmp     apak_sigsetjmp
    .size apak__setjmp, . - apak__setjmp

    /* int apak_sigsetjmp(long *env, int saveMask) */
    .p2align 4
    .globl apak_sigsetjmp
    .hidden apak_sigsetjmp
    .type apak_sigsetjmp, @function
apak_sigsetjmp:
    movq    %rbx, 0(%rdi)
    movq    %rbp, 8(%rdi)
    movq    %r12, 16(%rdi)
    movq    %r13, 24(%rdi)
    movq    %r14, 32(%rdi)
    movq    %r15, 40(%rdi)
    leaq    8(%rsp), %rdx
    movq    %rdx, 48(%rdi)
    movq    (%rsp), %rdx
    movq    %rdx, 56(%rdi)
    movslq  %esi, %rsi
    movq    %rsi, 64(%rdi)
    testq   %rsi, %rsi
    jz      1f
    /* rt_sigprocmask(SIG_BLOCK, NULL, &env[9], 8) */
    movq    %rdi, %r8
    leaq    72(%rdi), %rdx
    xorl    %esi, %esi
    xorl    %edi, %edi
    movl    $8, %r10d
    movl    $14, %eax
    syscall
1:
    xorl    %eax, %eax
    ret
    .size apak_sigsetjmp, . - apak_sigsetjmp

    /* void apak_siglongjmp(long *env, int value) — also longjmp/_longjmp */
    .p2align 4
    .globl apak_siglongjmp
    .hidden apak_siglongjmp
    .type apak_siglongjmp, @function
    .globl apak_longjmp
    .hidden apak_longjmp
    .type apak_longjmp, @function
    .globl apak__longjmp
    .hidden apak__longjmp
    .type apak__longjmp, @function
apak_siglongjmp:
apak_longjmp:
apak__longjmp:
    movq    %rdi, %r8
    movl    %esi, %r9d
    cmpq    $0, 64(%r8)
    je      2f
    /* rt_sigprocmask(SIG_SETMASK, &env[9], NULL, 8) */
    movl    $2, %edi
    leaq    72(%r8), %rsi
    xorl    %edx, %edx
    movl    $8, %r10d
    movl    $14, %eax
    syscall
2:
    movq    0(%r8), %rbx
    movq    8(%r8), %rbp
    movq    16(%r8), %r12
    movq    24(%r8), %r13
    movq    32(%r8), %r14
    movq    40(%r8), %r15
    movq    48(%r8), %rsp
    movl    %r9d, %eax
    testl   %eax, %eax
    jnz     3f
    movl    $1, %eax
3:
    jmp     *56(%r8)
    .size apak_siglongjmp, . - apak_siglongjmp

    .section .note.GNU-stack, "", @progbits

#else
#error "bionicjmp.S supports aarch64 and x86_64"
#endif
