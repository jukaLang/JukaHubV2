# USB controller (the handheld as a controller over a cable)

Plug the handheld into a computer with a USB-C data cable and use its buttons,
D-pad and sticks as a controller there: for PC games, Steam, an emulator on the
computer, or a phone or tablet that takes USB controllers.

Open **Tools → USB controller**, or **Settings → Network → USB controller**. It
is there on the handheld only; on a PC there is nothing to turn on.

[Controller Mode](CONTROLLER_MODE.md) (Tools → Controller Mode) offers the same
USB link next to Wi-Fi and Bluetooth, with its layouts and live display; this
screen is the USB port's own check and tester. The two never hold the port at
once: whichever started first keeps it.

## What this kernel offers

The screen first checks the firmware's kernel and says what it found:

- **USB device port**: the kernel's USB gadget support and the port's device
  controller. Without them the port can only be a host and the USB controller
  cannot start.
- **Kernel HID gadget**: the kernel's own HID function. The Generic HID shape
  uses it when it is there.
- **FunctionFS**: lets JukaHub be a USB function itself, the way
  [USB file transfer](USB_FILE_TRANSFER.md) does. The Xbox 360 shape needs it,
  and Generic HID falls back to it when the kernel has no HID function.

The check makes a test gadget with those functions and removes it at once. The
log has the same answer in one line (`usbpad: controller mode check: ...`),
with the kernel's own settings when `/proc/config.gz` exists.

## Choosing how the computer sees it

- **PC (Xbox 360 compatible)**: the handheld shows up as a wired Xbox 360
  controller. Windows installs its own driver and XInput games and Steam work
  as with a real one. Linux works too. When a game asks for rumble, the
  handheld's motor runs. The **Vibration** switch in Settings turns that off.
- **Generic HID gamepad**: a standard USB game controller with 16 buttons, a
  hat (the D-pad) and two sticks. macOS, Android, Linux and older DirectInput
  games read it without a driver. There is no rumble. Buttons follow the usual
  Linux and Android order: 1 A, 2 B, 4 X, 5 Y, 7 and 8 the shoulders, 9 and 10
  the triggers, 11 Select, 12 Start, 13 Menu, 14 and 15 the stick clicks.

**Letters by position** (the default) puts A on the bottom face button, the way
an Xbox pad has it, so on-screen prompts match where your thumb goes. **Letters
as printed** makes the button printed A send A, with the A on the right.

## Using it

Press **Start USB controller**, then plug in the cable (or have it plugged in
already). The screen shows:

- **USB controller — waiting for cable** until a computer sets the controller
  up. In this state the handheld's buttons still work in JukaHub, and **B**
  stops it.
- **USB controller — connected to PC** once a computer uses it. From then on
  every button goes to the computer, and JukaHub's own shortcuts (the quit
  chord, the Menu button, mouse mode) do nothing. **Hold Select + Start for 2
  seconds** to leave. The computer never sees that chord: once
  both buttons are down, neither is sent.

Under the status line, a tester lights each button and moves the stick dots as
the computer gets them. Shoulder and trigger bars fill as you press. **Rumble**
shows while the computer asks for vibration.

To save battery, the screen dims after 8 seconds without input and comes back
on the next press. The handheld does not go to sleep while the USB controller is
on. The USB controller turns itself off after 10 minutes without a computer.

## The USB port

Only one thing can use the port at a time:

- **USB file transfer** pauses while the USB controller is on and comes back on
  its own when it ends. The same goes for Browser over USB.
- **Host mode (OTG)**: the USB controller cannot start while the port is in Host
  mode. Set it to Device or Auto in [USB & OTG](USB_OTG.md) first.
- **ADB**: the firmware's own USB gadget is set aside while the USB controller is
  on and comes back when it ends. It also comes back if JukaHub quits, and if
  JukaHub crashes, at its next start.

The USB controller stops when you leave the screen, hold the chord, press B while
waiting, or quit JukaHub. Each way gives the port back.

## Limits

- Not verified on every model yet. Whether the kernel has the HID gadget is
  checked on the handheld itself and shown on the screen.
- Most kernels' FunctionFS refuses the extra descriptor that a real Xbox 360
  controller carries. JukaHub then offers the controller without it, and the
  screen says so. If Windows lists the controller with a warning, use Generic
  HID.
- The Brick has no sticks, so its stick axes stay centred.
- Rumble works in the Xbox 360 shape only.
