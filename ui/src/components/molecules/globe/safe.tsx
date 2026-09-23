import {Component, ReactNode, Suspense, lazy} from 'react'
import GlobeSuspense from './suspense'
import {Targets} from '../../../constants'
import {hasWebGL} from '../../../utils/capabilities'

const Globe = lazy(() => import('.'))

interface BoundaryProps {
	children: ReactNode
}

// The widget has no other error boundary, so without this a throw from the
// globe unmounts the whole widget, toggle included. That is what happens when
// three.js cannot create a WebGL context, or when the globe chunk fails to load.
// Rendering nothing matches the existing globe={false} layout.
class GlobeBoundary extends Component<BoundaryProps, {failed: boolean}> {
	state = {failed: false}

	static getDerivedStateFromError() {
		return {failed: true}
	}

	componentDidCatch(error: Error) {
		console.warn('globe disabled:', error.message)
	}

	render() {
		return this.state.failed ? null : this.props.children
	}
}

// Without WebGL the globe can only fail, so skip it before fetching its chunk.
const SafeGlobe = ({target}: {target: Targets}) => {
	if (!hasWebGL()) return null
	return (
		<GlobeBoundary>
			<Suspense fallback={<GlobeSuspense/>}>
				<Globe target={target}/>
			</Suspense>
		</GlobeBoundary>
	)
}

export default SafeGlobe
