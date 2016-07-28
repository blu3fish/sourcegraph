import React from "react";
import DesktopHome, {NotInBeta} from "sourcegraph/desktop/DesktopHome";

import {rel} from "sourcegraph/app/routePatterns";
import {inBeta} from "sourcegraph/user";
import * as betautil from "sourcegraph/util/betautil";
import {getRouteName} from "sourcegraph/app/routePatterns";

export const desktopHome = {
    getComponent: (location, callback) => {
        require.ensure([], (require) => {
            callback(null, {
                main: require("sourcegraph/desktop/DesktopHome").default,
            });
        });
    },
};

export const routes: Array<Route> = [
    {
        ...desktopHome,
        path: rel.desktopHome,
    },
];

export default function desktopRouter(Component: ReactClass<any>): ReactClass<any> {
    class DesktopRouter extends React.Component {
        static contextTypes = {
            router: React.PropTypes.object.isRequired,
            user: React.PropTypes.object,
            signedIn: React.PropTypes.bool.isRequired,
        };

        static propTypes = {
            routes: React.PropTypes.array,
        };

        constructor(props) {
            super(props);
            this.DesktopClient = navigator.userAgent.includes("Electron");
        }

        render() {
            if (!this.DesktopClient) {
                return <Component {...this.props} />;
            };

            const inbeta = inBeta(this.context.user, betautil.DESKTOP);
            // Include this.context.user to prevent flicker when user loads
            if (this.context.signedIn && this.context.user && !inbeta) {
                return <NotInBeta />;
            }

            if (getRouteName(this.props.routes) === "home") {
                if (!this.context.signedIn) {
                    // Prevent unauthed users from escaping
                    this.context.router.replace(rel.login);
                } else {
                    this.context.router.replace(rel.desktopHome);
                }
            }

            return <Component {...this.props} />;
        }
    };

    return DesktopRouter;
}
