import React from 'react'
import {Layouts, Settings} from './index'
import Layout from './layout'
import Toast from './components/molecules/toast'
import Banner from './components/organisms/banner'
import Panel from './components/organisms/panel'

export interface AppState {
  isSharing: boolean
}

interface Props {
  settings: Settings
}

const App = ({settings}: Props) => {
  return (
    <Layout
      theme={settings.theme}
      layout={settings.layout}
    >
      { settings.features.toast && <Toast /> }
      { settings.layout === Layouts.BANNER && (
        <Banner
          settings={settings}
        />
      )}
      { settings.layout === Layouts.PANEL && (
        <Panel
          settings={settings}
        />
      )}
    </Layout>
  );
}

export default App;