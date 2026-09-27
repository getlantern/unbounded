import {Component, ReactNode, Suspense, lazy} from 'react'
import GlobeSuspense from './suspense'
import Col from '../../atoms/col'
import {Targets} from '../../../constants'
import {hasWebGL} from '../../../utils/capabilities'

const Globe = lazy(() => import('.'))

interface BoundaryProps {
	children: ReactNode
}

// The widget has no other error boundary, so without this a throw from the
// globe unmounts the whole widget, toggle included. That is what happens when
// three.js cannot create a WebGL context, or when the globe chunk fails to load.
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

// Owns the globe's column so that a skipped or failed globe removes the column
// too, exactly like globe={false}. An empty column would still take half the
// row in the desktop layouts. Without WebGL the globe can only fail, so skip it
// before fetching its chunk.
const SafeGlobe = ({target}: {target: Targets}) => {
	if (!hasWebGL()) return null
	return (
		<GlobeBoundary>
			<Col>
				<Suspense fallback={<GlobeSuspense/>}>
					<Globe target={target}/>
				</Suspense>
			</Col>
		</GlobeBoundary>
	)
}

export default SafeGlobe
