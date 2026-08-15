# JukaHub

[![Netlify Status](https://api.netlify.com/api/v1/badges/dea0cf4b-3799-41d9-8f2a-b4fd2a270b15/deploy-status)](https://generate.jukalang.com)

**Juka GUI Creator**

If you just want to create the app, launch it from here: [https://generate.jukalang.com](https://generate.jukalang.com)

**Join the Conversation:** [Discord Chat](https://discord.gg/R9qgJjh5jG)

## Overview

JukaHub is a powerful tool designed to streamline the process of creating graphical user interfaces (GUIs) for Trimui Smart Pro and Trimui Brick. This project is split into two primary components:

1. **Website with Drag/Drop Interface**: This interface allows users to generate `jukaconfig.json` code effortlessly.
2. **Player Application**: Developed in Go, this player takes the `jukaconfig.json` file and launches the application accordingly.

## Project Structure

The project is structured into two main files:

- **Index.html**: Utilized for generating the GUI via a drag-and-drop interface.
- **Player**: Executes the `jukaconfig.json` files, effectively running the apps.

## Getting Started

### Prerequisites

To set up your development environment, ensure you have the following installed:

- [Go](https://golang.org/dl/)
- [SDL2](https://github.com/veandco/go-sdl2/)

### Installation

1. **Drag/Drop GUI:**
   - Open `index.html` and modify it to suit your requirements.

2. **Player:**
   - Follow the instructions to install Go.
   - Install SDL2 by following the steps on [go-sdl2](https://github.com/veandco/go-sdl2/).
   - Make necessary changes to the Go code as per your needs.

## Usage

### Creating the GUI

To create the GUI using the drag-and-drop interface:

1. Open `index.html` in your preferred web browser.
2. Utilize the drag-and-drop elements to design your GUI.
3. Save the configuration, which will be generated in the `jukaconfig.json` file.

### Running the Application

To run the application using the player:

1. Ensure you have the `jukaconfig.json` file ready.
2. Use the Go player to execute the configuration:
   ```sh
   go run player.go
   ```
3. The application should launch as per the configurations defined in the jukaconfig.json file.


## Contributing

We welcome contributions from the community. If you wish to contribute, please follow these steps:

1. Fork the repository.
2. Create a new branch (`git checkout -b feature-branch`).
3. Make your modifications.
4. Commit your changes (`git commit -m 'Add feature'`).
5. Push to the branch (`git push origin feature-branch`).
6. Open a pull request.

## Support

If you encounter any issues or have questions, feel free to join our [Discord Chat](https://discord.gg/R9qgJjh5jG) or raise an issue in the repository.

## License

This project is licensed under the MIT License. See the [LICENSE](https://github.com/jukaLang/JukaHubV2/blob/main/LICENSE) file for details.

## Special Thanks
Special thanks to Nevrdid for finding and documenting bugs
Special thanks to Arthur Gregorio for answering questions regarding Go structure.
Special thanks to StarDrive and DigestPrism for supporting this app.
Special thanks to all of Juka Discord channel for making this app possible.

