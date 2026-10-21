// gallery/layouts.js — community layouts bundled with the builder.
//
// Each entry is a complete jukaconfig.json, so Fork and Apply go through exactly
// the same import/push path as a file. Add a layout by appending an object here
// and opening a pull request; keep it 1280x720 and under ~20 elements so it
// previews well on the handheld and in the gallery thumbnail.

window.JUKA_LAYOUTS = [
  {
    id: 'aurora-bento',
    name: 'Aurora Bento',
    author: '@mika',
    description: 'A calm dashboard with six tinted tiles and a second scene for media.',
    tags: ['Home', 'Dark', 'Bento'],
    accent: '#25c2a0',
    config: {
      AppName: 'JukaHub',
      Version: '0.4.0',
      Width: 1280,
      Height: 720,
      title: 'Aurora Bento',
      author: 'mika',
      description: 'Bento dashboard layout.',
      variables: { fontSizes: { title: 48, big: 34, medium: 24, small: 18 } },
      scenes: [
        {
          name: 'Home',
          background: '#0B0F17',
          elements: [
            { type: 'label', text: 'Good evening', x: 48, y: 44, width: 700, height: 64, font: 'title', color: '#F8FAFC' },
            { type: 'label', text: 'JukaHub · TrimUI Smart Pro', x: 48, y: 112, width: 520, height: 32, font: 'small', color: '#8FA0B8' },
            { type: 'button', text: 'Media', x: 48, y: 176, width: 360, height: 196, font: 'big', color: '#F8FAFC', bgColor: '#1E3A8A', trigger: 'change_scene', sceneChange: 'Media' },
            { type: 'button', text: 'Files', x: 432, y: 176, width: 360, height: 196, font: 'big', color: '#F8FAFC', bgColor: '#0F766E' },
            { type: 'button', text: 'Chat', x: 816, y: 176, width: 416, height: 196, font: 'big', color: '#F8FAFC', bgColor: '#5B21B6' },
            { type: 'button', text: 'Settings', x: 48, y: 396, width: 360, height: 176, font: 'big', color: '#F8FAFC', bgColor: '#334155' },
            { type: 'button', text: 'Favorites', x: 432, y: 396, width: 360, height: 176, font: 'big', color: '#F8FAFC', bgColor: '#9A3412' },
            { type: 'button', text: 'Search / Tools', x: 816, y: 396, width: 416, height: 176, font: 'big', color: '#F8FAFC', bgColor: '#0E7490' },
            { type: 'label', text: 'A / select    B / back', x: 48, y: 618, width: 500, height: 36, font: 'small', color: '#77839A' }
          ]
        },
        {
          name: 'Media',
          background: '#0B0F17',
          elements: [
            { type: 'label', text: 'Media', x: 48, y: 44, width: 500, height: 64, font: 'title', color: '#F8FAFC' },
            { type: 'button', text: 'Albums', x: 48, y: 150, width: 280, height: 180, font: 'big', color: '#F8FAFC', bgColor: '#1E3A8A' },
            { type: 'button', text: 'Playlists', x: 352, y: 150, width: 280, height: 180, font: 'big', color: '#F8FAFC', bgColor: '#0F766E' },
            { type: 'button', text: 'Radio', x: 656, y: 150, width: 280, height: 180, font: 'big', color: '#F8FAFC', bgColor: '#9A3412' },
            { type: 'button', text: 'Videos', x: 960, y: 150, width: 272, height: 180, font: 'big', color: '#F8FAFC', bgColor: '#5B21B6' },
            { type: 'button', text: 'Back', x: 48, y: 396, width: 220, height: 72, font: 'medium', color: '#F8FAFC', bgColor: '#334155', trigger: 'change_scene', sceneChange: 'Home' }
          ]
        }
      ]
    }
  },
  {
    id: 'midnight-arcade',
    name: 'Midnight Arcade',
    author: '@retro_dan',
    description: 'High-contrast neon tiles for arcade cabinets and shmups.',
    tags: ['Arcade', 'Dark', 'Neon'],
    accent: '#ff4d9d',
    config: {
      AppName: 'JukaHub',
      Version: '0.4.0',
      Width: 1280,
      Height: 720,
      title: 'Midnight Arcade',
      author: 'retro_dan',
      description: 'Neon arcade home screen.',
      variables: { fontSizes: { title: 52, big: 34, medium: 24, small: 18 } },
      scenes: [
        {
          name: 'Arcade',
          background: '#0A0614',
          elements: [
            { type: 'label', text: 'MIDNIGHT ARCADE', x: 64, y: 40, width: 900, height: 76, font: 'title', color: '#FF4D9D' },
            { type: 'label', text: 'INSERT COIN', x: 64, y: 118, width: 420, height: 32, font: 'small', color: '#7DE2FF' },
            { type: 'button', text: 'Play', x: 64, y: 182, width: 556, height: 220, font: 'big', color: '#0A0614', bgColor: '#7DE2FF', trigger: 'change_scene', sceneChange: 'Library' },
            { type: 'button', text: 'Library', x: 660, y: 182, width: 556, height: 220, font: 'big', color: '#0A0614', bgColor: '#FF4D9D', trigger: 'change_scene', sceneChange: 'Library' },
            { type: 'button', text: 'Scores', x: 64, y: 432, width: 556, height: 160, font: 'big', color: '#F8FAFC', bgColor: '#3B1D5E' },
            { type: 'button', text: 'Settings', x: 660, y: 432, width: 556, height: 160, font: 'big', color: '#F8FAFC', bgColor: '#1D3B5E' },
            { type: 'label', text: 'A / start    B / back    X / favourites', x: 64, y: 626, width: 720, height: 36, font: 'small', color: '#8C7BAA' }
          ]
        },
        {
          name: 'Library',
          background: '#0A0614',
          elements: [
            { type: 'label', text: 'LIBRARY', x: 64, y: 44, width: 600, height: 72, font: 'title', color: '#7DE2FF' },
            { type: 'button', text: 'Favourites', x: 64, y: 160, width: 360, height: 200, font: 'big', color: '#F8FAFC', bgColor: '#3B1D5E' },
            { type: 'button', text: 'Recently played', x: 456, y: 160, width: 360, height: 200, font: 'big', color: '#F8FAFC', bgColor: '#1D3B5E' },
            { type: 'button', text: 'All games', x: 848, y: 160, width: 368, height: 200, font: 'big', color: '#0A0614', bgColor: '#FF4D9D' },
            { type: 'button', text: 'Back', x: 64, y: 420, width: 220, height: 72, font: 'medium', color: '#0A0614', bgColor: '#7DE2FF', trigger: 'change_scene', sceneChange: 'Arcade' }
          ]
        }
      ]
    }
  },
  {
    id: 'paper-mono',
    name: 'Paper Mono',
    author: '@lumen',
    description: 'Warm paper background, two oversized choices. Easy on the eyes at night.',
    tags: ['Minimal', 'Light', 'Reading'],
    accent: '#1F2937',
    config: {
      AppName: 'JukaHub',
      Version: '0.4.0',
      Width: 1280,
      Height: 720,
      title: 'Paper Mono',
      author: 'lumen',
      description: 'Minimal light layout.',
      variables: { fontSizes: { title: 56, big: 36, medium: 24, small: 18 } },
      scenes: [
        {
          name: 'Home',
          background: '#F5F1E8',
          elements: [
            { type: 'label', text: 'Studio', x: 72, y: 64, width: 700, height: 80, font: 'title', color: '#1F2937' },
            { type: 'label', text: 'Read, listen, make.', x: 72, y: 144, width: 520, height: 34, font: 'medium', color: '#6B6255' },
            { type: 'button', text: 'Library', x: 72, y: 220, width: 540, height: 260, font: 'big', color: '#F9F6EF', bgColor: '#1F2937', trigger: 'change_scene', sceneChange: 'Library' },
            { type: 'button', text: 'Now playing', x: 668, y: 220, width: 540, height: 260, font: 'big', color: '#1F2937', bgColor: '#D8CCB8' },
            { type: 'label', text: 'JukaHub for TrimUI · paper edition', x: 72, y: 628, width: 620, height: 32, font: 'small', color: '#8A8072' }
          ]
        },
        {
          name: 'Library',
          background: '#F5F1E8',
          elements: [
            { type: 'label', text: 'Library', x: 72, y: 60, width: 600, height: 76, font: 'title', color: '#1F2937' },
            { type: 'label', text: 'Books', x: 72, y: 170, width: 500, height: 90, font: 'big', color: '#1F2937' },
            { type: 'label', text: 'Articles', x: 72, y: 300, width: 500, height: 90, font: 'big', color: '#1F2937' },
            { type: 'label', text: 'Notes', x: 72, y: 430, width: 500, height: 90, font: 'big', color: '#1F2937' },
            { type: 'button', text: 'Back', x: 1000, y: 600, width: 208, height: 72, font: 'medium', color: '#F9F6EF', bgColor: '#1F2937', trigger: 'change_scene', sceneChange: 'Home' }
          ]
        }
      ]
    }
  },
  {
    id: 'media-deck',
    name: 'Media Deck',
    author: '@pixelpete',
    description: 'Search-first layout for YouTube, podcasts and IPTV on the couch.',
    tags: ['Media', 'Dark', 'Search'],
    accent: '#0E7490',
    config: {
      AppName: 'JukaHub',
      Version: '0.4.0',
      Width: 1280,
      Height: 720,
      title: 'Media Deck',
      author: 'pixelpete',
      description: 'Search-first media layout.',
      variables: { fontSizes: { title: 46, big: 32, medium: 24, small: 18 } },
      scenes: [
        {
          name: 'Home',
          background: '#080C12',
          elements: [
            { type: 'label', text: 'Media deck', x: 48, y: 40, width: 600, height: 60, font: 'title', color: '#F8FAFC' },
            { type: 'input', text: '', placeholder: 'Search or paste a link', x: 48, y: 124, width: 880, height: 64, font: 'medium', color: '#F8FAFC', bgColor: '#141A26', variable: 'search_query' },
            { type: 'button', text: 'Search', x: 952, y: 124, width: 280, height: 64, font: 'medium', color: '#F8FAFC', bgColor: '#0E7490', trigger: 'youtube_search' },
            { type: 'button', text: 'YouTube', x: 48, y: 228, width: 280, height: 200, font: 'big', color: '#F8FAFC', bgColor: '#9A3412', trigger: 'change_scene', sceneChange: 'Tube' },
            { type: 'button', text: 'Podcasts', x: 352, y: 228, width: 280, height: 200, font: 'big', color: '#F8FAFC', bgColor: '#5B21B6' },
            { type: 'button', text: 'IPTV', x: 656, y: 228, width: 280, height: 200, font: 'big', color: '#F8FAFC', bgColor: '#1E3A8A' },
            { type: 'button', text: 'Shorts', x: 960, y: 228, width: 272, height: 200, font: 'big', color: '#F8FAFC', bgColor: '#0F766E' },
            { type: 'button', text: 'Recent', x: 48, y: 452, width: 592, height: 148, font: 'big', color: '#F8FAFC', bgColor: '#334155' },
            { type: 'button', text: 'Favourites', x: 664, y: 452, width: 568, height: 148, font: 'big', color: '#F8FAFC', bgColor: '#7C3AED' }
          ]
        },
        {
          name: 'Tube',
          background: '#080C12',
          elements: [
            { type: 'label', text: 'YouTube', x: 48, y: 40, width: 500, height: 60, font: 'title', color: '#F8FAFC' },
            { type: 'searchresults', text: '', x: 48, y: 130, width: 1184, height: 480, columns: 3, rows: 2, color: '#F8FAFC' },
            { type: 'button', text: 'Back', x: 48, y: 628, width: 220, height: 64, font: 'medium', color: '#F8FAFC', bgColor: '#334155', trigger: 'change_scene', sceneChange: 'Home' }
          ]
        }
      ]
    }
  },
  {
    id: 'cabinet',
    name: 'Cabinet',
    author: '@juka_community',
    description: 'Warm wood-and-orange cabinet theme with chunky thumb-sized buttons.',
    tags: ['Retro', 'Dark', 'Large text'],
    accent: '#F97316',
    config: {
      AppName: 'JukaHub',
      Version: '0.4.0',
      Width: 1280,
      Height: 720,
      title: 'Cabinet',
      author: 'juka_community',
      description: 'Large touch targets, cabinet colours.',
      variables: { fontSizes: { title: 50, big: 36, medium: 26, small: 20 } },
      scenes: [
        {
          name: 'Home',
          background: '#22140D',
          elements: [
            { type: 'label', text: 'CABINET', x: 56, y: 40, width: 600, height: 72, font: 'title', color: '#FDBA74' },
            { type: 'label', text: 'Pick a cabinet', x: 56, y: 112, width: 480, height: 34, font: 'small', color: '#C9A183' },
            { type: 'button', text: 'Play', x: 56, y: 180, width: 552, height: 236, font: 'big', color: '#22140D', bgColor: '#F97316', trigger: 'change_scene', sceneChange: 'Games' },
            { type: 'button', text: 'Favourites', x: 640, y: 180, width: 584, height: 236, font: 'big', color: '#FDBA74', bgColor: '#8A4B1F' },
            { type: 'button', text: 'Settings', x: 56, y: 444, width: 552, height: 168, font: 'big', color: '#FDBA74', bgColor: '#5B3218' },
            { type: 'button', text: 'Power', x: 640, y: 444, width: 584, height: 168, font: 'big', color: '#FDBA74', bgColor: '#5B3218' }
          ]
        },
        {
          name: 'Games',
          background: '#22140D',
          elements: [
            { type: 'label', text: 'All games', x: 56, y: 40, width: 600, height: 72, font: 'title', color: '#FDBA74' },
            { type: 'button', text: 'Continue', x: 56, y: 150, width: 552, height: 190, font: 'big', color: '#22140D', bgColor: '#F97316' },
            { type: 'button', text: 'By system', x: 640, y: 150, width: 584, height: 190, font: 'big', color: '#FDBA74', bgColor: '#5B3218' },
            { type: 'button', text: 'Random', x: 56, y: 372, width: 552, height: 150, font: 'big', color: '#FDBA74', bgColor: '#5B3218' },
            { type: 'button', text: 'Back', x: 640, y: 372, width: 584, height: 150, font: 'big', color: '#22140D', bgColor: '#FDBA74', trigger: 'change_scene', sceneChange: 'Home' }
          ]
        }
      ]
    }
  }
];
